package com.nextflix.tv.feature.subtitles

import com.nextflix.tv.core.Core
import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.prefs.SubtitleChoice
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.channels.BufferOverflow
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

/** What the controller needs from the app (a fake in tests). */
internal interface SubtitleBackend {
    suspend fun list(query: SubtitleQuery, refresh: Boolean): List<SubtitleTrack>
    suspend fun cues(id: String, at: Double?): CueFile

    /** The profile's last subtitle choice (PlayerPrefs.subtitle). */
    fun choice(): SubtitleChoice?

    /** Saves the choice; [choice] must read it back at once. */
    fun saveChoice(choice: SubtitleChoice)

    suspend fun savedDelay(hash: String, index: Int): Double

    /** Saves in the background, in call order. */
    fun saveDelay(hash: String, index: Int, seconds: Double)
}

private class CoreBackend(private val core: Core) : SubtitleBackend {
    private val repository = SubtitleRepository(core)
    private val timing = SubtitleTimingStore(core)
    private val writes = Mutex()

    override suspend fun list(query: SubtitleQuery, refresh: Boolean) = repository.list(query, refresh)
    override suspend fun cues(id: String, at: Double?) = repository.cues(id, at)
    override fun choice(): SubtitleChoice? = core.playerPrefs.get(core.session.profileId).subtitle

    // Undispatched: the in-memory prefs change before this returns, and writes queue on the mutex in call order
    override fun saveChoice(choice: SubtitleChoice) {
        val pid = core.session.profileId
        core.scope.launch(start = CoroutineStart.UNDISPATCHED) {
            core.playerPrefs.update(pid) { it.copy(subtitle = choice) }
        }
    }

    override suspend fun savedDelay(hash: String, index: Int) = timing.get(hash, index)

    override fun saveDelay(hash: String, index: Int, seconds: Double) {
        core.scope.launch(start = CoroutineStart.UNDISPATCHED) {
            writes.withLock { timing.set(hash, index, seconds) }
        }
    }
}

/**
 * Subtitle behaviour of one playback, driven by the player. Owns the rules of the webOS player
 * (player/engine.ts subtitle parts, lib/subtitles.ts):
 *
 * - list on [start], ask again after 8 s and 20 s while the profile's language is missing, then every
 *   LATE_SUBTITLE_POLL_MS with refresh; merge lists, and when the viewer has not chosen, switch to a subtitle of
 *   the profile's language that arrived late (lateSubtitles rules);
 * - default pick: the profile's last choice (PlayerPrefs.subtitle: off / language + id), else for a Turkish profile
 *   Turkish unless the audio is Turkish (pickSubtitle);
 * - load cues of the selected subtitle; poll partial files (live tracks, timing in progress) every 15 s with the
 *   position, AI translations every 3 s; a subtitle without cues is replaced by the next one of its language
 *   (another provider first);
 * - [select] saves the viewer's choice in PlayerPrefs; [setDelay] changes the timing (±60 s, 0.5 s steps in the
 *   panel) and keeps it per file (SubtitleTimingStore).
 *
 * The player calls [start] once the playing file (hash / index) is known (every fresh open of a source),
 * [onPosition] about once a second, [restart] when it splices another file in seamlessly (the old subtitle stays
 * until the new list picks one), and [stop] when it closes. [events] asks for the subalt lookup and announces late
 * subtitles; [log] receives the `subs` / `subs-none` / `subdelay` playback log events.
 * All methods are meant for the main thread.
 */
class SubtitleController internal constructor(
    private val backend: SubtitleBackend,
    scope: CoroutineScope,
    dispatcher: CoroutineDispatcher,
    private val clock: () -> Long,
) {
    constructor(core: Core, scope: CoroutineScope) :
        this(CoreBackend(core), scope, Dispatchers.Main.immediate, System::currentTimeMillis)

    private val root = CoroutineScope(scope.coroutineContext + SupervisorJob(scope.coroutineContext[Job]) + dispatcher)

    private val _state = MutableStateFlow(SubtitleState())
    val state: StateFlow<SubtitleState> = _state.asStateFlow()

    private val _events = MutableSharedFlow<SubtitleEvent>(extraBufferCapacity = 8, onBufferOverflow = BufferOverflow.DROP_OLDEST)
    val events: SharedFlow<SubtitleEvent> = _events.asSharedFlow()

    /** Playback log sink (the player's log), null: not logged. */
    var log: SubtitleLogSink? = null

    private var query: SubtitleQuery? = null
    private var audioLang: String? = null
    private var position = 0.0

    // Everything of the playing file: list, retries, late checks, cue polling
    private var session: Job? = null
    private var cueJob: Job? = null
    private var list: List<SubtitleTrack> = emptyList()
    private var picked = false
    private var decided = false
    private var userPicked = false
    private var repick = false
    private var wantLang = ""
    private val failed = HashSet<String>()

    private val delayLog = SubsLog.DelayLog()
    private var delayLogJob: Job? = null
    private var delaySaveJob: Job? = null
    private var pendingDelaySave: (() -> Unit)? = null
    private var delayTouched = false

    /** Starts for the playing file. [audioLang]: language of the playing audio track (default pick rule). */
    fun start(query: SubtitleQuery, audioLang: String?) {
        newSession(query, audioLang)
        decided = false
        userPicked = false
        repick = false
        failed.clear()
        _state.value = SubtitleState(searching = true)
        loadDelay(query)
        loadList()
    }

    /** Another file plays now (upgrade, sidegrade, next source): list again for it, keep the language choice. */
    fun restart(query: SubtitleQuery, audioLang: String?) {
        newSession(query, audioLang)
        // The old file's subtitle stays until the new list picks one for this file
        repick = true
        _state.update { it.copy(delaySeconds = 0.0, translating = false) }
        loadDelay(query)
        loadList()
    }

    /**
     * The viewer switched to another release for its subtitles in [lang] (the subalt row): the next list that has
     * [lang] shows its best subtitle as the viewer's choice. Call before [start] / [restart] of that release.
     */
    fun preferLanguage(lang: String) {
        wantLang = Languages.norm(lang)
    }

    /** The viewer picked a subtitle (null = off). */
    fun select(id: String?) {
        decided = true
        userPicked = true
        val track = item(id)
        when {
            id == null -> backend.saveChoice(SubtitleChoice.Off)
            track != null -> backend.saveChoice(SubtitleChoice.Language(Languages.norm(track.language), track.id))
        }
        if (id == null) noteNone("off")
        apply(id, SubsLog.Why.User)
    }

    /** Subtitle timing in seconds (+ later), kept for the playing file. Returns the value taken (rounded, limited). */
    fun setDelay(seconds: Double): Double {
        val d = SubtitleRules.clampDelay(seconds)
        if (d == _state.value.delaySeconds) return d
        delayTouched = true
        _state.update { it.copy(delaySeconds = d) }
        delayLog.change(d, clock())
        delayLogJob?.cancel()
        delayLogJob = root.launch {
            delay(SubsLog.DELAY_SETTLE_MS + 50)
            noteDelay(delayLog.take(clock()))
        }
        val q = query ?: return d
        pendingDelaySave = { backend.saveDelay(q.hash, q.index, d) }
        delaySaveJob?.cancel()
        delaySaveJob = root.launch {
            delay(DELAY_SAVE_MS)
            flushDelaySave()
        }
        return d
    }

    /** Current playback position (s, file timeline); drives live-track and AI polling. */
    fun onPosition(seconds: Double) {
        if (seconds.isFinite() && seconds >= 0) position = seconds
    }

    /** The audio track changed (the default pick may change). */
    fun onAudioLanguage(lang: String?) {
        if (Languages.norm(lang) == Languages.norm(audioLang)) return
        audioLang = lang
        val q = query ?: return
        // Only while nothing was ever chosen: a Turkish profile drops Turkish subtitles for Turkish audio and back
        if (userPicked || !decided || list.isEmpty() || backend.choice() != null) return
        val id = SubtitleRules.pick(list, profileLanguage(q), null, lang)
        if (id != _state.value.selectedId) apply(id, SubsLog.Why.Auto)
    }

    fun stop() {
        session?.cancel()
        session = null
        cueJob = null
        delayLogJob?.cancel()
        noteDelay(delayLog.take(clock(), force = true))
        flushDelaySave()
        _state.update { it.copy(translating = false) }
    }

    // ---- list ----

    private fun newSession(query: SubtitleQuery, audioLang: String?) {
        session?.cancel()
        cueJob = null
        session = SupervisorJob(root.coroutineContext[Job])
        this.query = query
        this.audioLang = audioLang
        list = emptyList()
        picked = false
    }

    private fun launchInSession(block: suspend CoroutineScope.() -> Unit): Job? =
        session?.let { CoroutineScope(root.coroutineContext + it).launch(block = block) }

    private fun profileLanguage(q: SubtitleQuery) = AppLanguage.of(q.uiLang ?: q.preferred)

    private fun hasPreferred(items: List<SubtitleTrack>, q: SubtitleQuery) = SubtitleRules.hasLang(items, q.preferred)

    private fun loadDelay(q: SubtitleQuery) {
        delayTouched = false
        flushDelaySave()
        noteDelay(delayLog.reset(0.0, clock()))
        launchInSession {
            val d = SubtitleRules.clampDelay(backend.savedDelay(q.hash, q.index))
            if (delayTouched || query !== q || d == 0.0) return@launchInSession
            _state.update { it.copy(delaySeconds = d) }
            noteDelay(delayLog.reset(d, clock()))
        }
    }

    private fun loadList() {
        val q = query ?: return
        if (q.tmdbId <= 0) {
            setTracks(emptyList())
            return
        }
        launchInSession {
            val items = backend.list(q, refresh = false)
            show(q, items)
            if (hasPreferred(items, q)) return@launchInSession
            // The server answers with what is ready and keeps asking slower providers for its next call
            var left = SubtitleRepository.EARLY_RETRIES_MS.size
            for (ms in SubtitleRepository.EARLY_RETRIES_MS) {
                launch {
                    delay(ms)
                    if (!hasPreferred(list, q)) {
                        val more = backend.list(q, refresh = false)
                        if (more.isNotEmpty()) show(q, SubtitleRules.merge(list, more))
                    }
                    if (--left == 0) settled(q)
                }
            }
        }
    }

    private fun setTracks(items: List<SubtitleTrack>) {
        val q = query
        val menu = if (q == null) items else SubtitleRules.perLanguage(items, q.langs, profileLanguage(q))
        _state.update { it.copy(tracks = items, menu = menu, searching = false) }
    }

    private fun show(q: SubtitleQuery, items: List<SubtitleTrack>) {
        list = items
        setTracks(items)
        val rep = repick && items.isNotEmpty()
        if (rep) repick = false
        // The viewer switched to this release for its subtitle: show it as their choice
        if (wantLang.isNotEmpty() && SubtitleRules.hasLang(items, wantLang)) {
            val id = SubtitleRules.best(items, wantLang)
            backend.saveChoice(SubtitleChoice.Language(wantLang))
            wantLang = ""
            decided = true
            userPicked = true
            apply(id, SubsLog.Why.User)
            return
        }
        // Default pick; a later, fuller list picks again while nothing is shown and the viewer chose nothing
        if (!rep && (userPicked || (decided && _state.value.selectedId != null))) return
        decided = true
        val pref = backend.choice()
        val id = SubtitleRules.pick(items, profileLanguage(q), pref, audioLang)
        val why = if (picked) SubsLog.Why.Auto else SubsLog.Why.Open
        picked = true
        if (if (rep) id != _state.value.selectedId else id != null) apply(id, why)
        if (id == null && why == SubsLog.Why.Open) {
            noteNone("open", buildMap {
                if (pref == SubtitleChoice.Off) put("pref", "off")
                put("audio", SubsLog.token(audioLang, 8))
            })
        }
    }

    /** The profile's language is needed when the audio is in another one. */
    private fun needed(q: SubtitleQuery): Boolean {
        val audio = Languages.norm(audioLang).ifEmpty { Languages.norm(q.originalLanguage) }
        return !hasPreferred(list, q) && audio != Languages.norm(q.preferred)
    }

    /** After the early retries: still missing, ask for a release that carries it and check for late uploads. */
    private fun settled(q: SubtitleQuery) {
        if (!hasPreferred(list, q)) noteNone("missing", mapOf("audio" to SubsLog.token(audioLang, 8)))
        if (!needed(q)) return
        _events.tryEmit(SubtitleEvent.Missing(q.preferred))
        lateCheck(q)
    }

    /** Late uploads: while missing, the list is asked again every 10 minutes (caches skipped). */
    private fun lateCheck(q: SubtitleQuery) {
        launchInSession {
            delay(SubtitleRepository.LATE_SUBTITLE_POLL_MS)
            if (hasPreferred(list, q)) return@launchInSession
            val more = backend.list(q, refresh = true)
            // An AI translation picked by itself gives way to a real subtitle
            val shown = _state.value.selected
            val viewerChose = userPicked || (_state.value.selectedId != null && shown?.isAi != true)
            val late = SubtitleRules.late(list, more, q.preferred, viewerChose)
            if (!late.arrived) {
                if (more.isNotEmpty()) show(q, late.items)
                _events.tryEmit(SubtitleEvent.Missing(q.preferred))
                lateCheck(q)
                return@launchInSession
            }
            list = late.items
            setTracks(late.items)
            if (late.pick != null) {
                decided = true
                apply(late.pick, SubsLog.Why.Auto)
            }
            _events.tryEmit(SubtitleEvent.Found(q.preferred))
        }
    }

    // ---- cues ----

    private fun item(id: String?): SubtitleTrack? = id?.let { i -> _state.value.tracks.firstOrNull { it.id == i } }

    private fun apply(id: String?, why: SubsLog.Why) {
        cueJob?.cancel()
        cueJob = null
        _state.update { it.copy(selectedId = id, cues = emptyList(), translating = false, sync = "") }
        if (id == null) return
        cueJob = launchInSession { loadCues(id, why) }
    }

    // Asked again while partial: a live embedded track (read from the position on, it grows), a subtitle the server
    // still times to the file and an AI translation alike; the final answer stops it
    private suspend fun loadCues(id: String, why: SubsLog.Why) {
        var loads = 0
        var errors = 0
        var logged: SubsLog.Shown? = null
        var loggedAt = 0L
        while (true) {
            val f = backend.cues(id, position)
            if (_state.value.selectedId != id) return
            when (SubtitleRules.cueAnswer(f.cues.size, f.partial, loads)) {
                SubtitleRules.CueAnswer.Retry -> {
                    delay(SubtitleRepository.LIVE_CUES_POLL_MS)
                    continue
                }
                SubtitleRules.CueAnswer.Fallback -> {
                    // Nothing came (an OpenSubtitles quota answers 429): the next one of the language, another provider first
                    failed.add(id)
                    val alt = SubtitleRules.fallback(_state.value.tracks, id, failed)
                    if (alt != null) {
                        apply(alt, SubsLog.Why.Fallback)
                        return
                    }
                    if (errors == 0) noteNone("failed", mapOf("id" to SubsLog.token(id, 24)))
                    // A server or network error without another subtitle to show: the same one again soon
                    if (!f.failed) return
                    delay(SubtitleRules.retryMs(++errors))
                    continue
                }
                SubtitleRules.CueAnswer.Show -> Unit
            }
            errors = 0
            failed.remove(id)
            loads++
            // Logged when it starts and when an answer moved, replaced or finished the cues (not when a live track grew)
            val next = SubsLog.Shown(id, f.cues, f.partial, f.sync)
            val now = clock()
            val change = SubsLog.change(logged, next, now - loggedAt)
            if (change.log) {
                note("subs", SubsLog.subsFields(item(id), next, if (logged != null) SubsLog.Why.Poll else why, _state.value.delaySeconds, change))
                logged = next
                loggedAt = now
            }
            // The overlay looks the cue up again in the new list; the delay stays
            _state.update {
                it.copy(
                    translating = f.translating,
                    cues = if (f.cues.isNotEmpty() || !f.partial) f.cues else it.cues,
                    sync = f.sync,
                )
            }
            if (!f.partial) return
            delay(if (f.translating) SubtitleRepository.AI_CUES_POLL_MS else SubtitleRepository.LIVE_CUES_POLL_MS)
        }
    }

    // ---- log ----

    private fun note(event: String, fields: Map<String, Any>) {
        log?.note(event, fields)
    }

    private fun noteNone(why: String, extra: Map<String, Any> = emptyMap()) {
        val lang = query?.let { it.uiLang ?: it.preferred }.orEmpty()
        note("subs-none", SubsLog.noneFields(why, lang, _state.value.tracks, extra))
    }

    private fun noteDelay(change: SubsLog.DelayChange?) {
        change ?: return
        val id = _state.value.selectedId
        note("subdelay", buildMap {
            put("subdelay", change.subdelay)
            put("from", change.from)
            id?.let { put("id", SubsLog.token(it, 24)) }
            item(id)?.let { put("provider", SubsLog.token(it.source, 16)) }
        })
    }

    private fun flushDelaySave() {
        delaySaveJob?.cancel()
        delaySaveJob = null
        pendingDelaySave?.invoke()
        pendingDelaySave = null
    }

    private companion object {
        const val DELAY_SAVE_MS = 500L
    }
}
