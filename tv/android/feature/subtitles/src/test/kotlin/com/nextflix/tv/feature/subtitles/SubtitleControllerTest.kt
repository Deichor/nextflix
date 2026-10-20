package com.nextflix.tv.feature.subtitles

import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.prefs.SubtitleChoice
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.toList
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class SubtitleControllerTest {
    private class FakeBackend : SubtitleBackend {
        val lists = ArrayDeque<List<SubtitleTrack>>()
        var listCalls = 0
        var listDelayMs = 0L
        var refreshCalls = 0
        val files = HashMap<String, ArrayDeque<CueFile>>()
        val cueCalls = ArrayList<Pair<String, Double?>>()
        val pretimed = ArrayList<String>()
        var saved: SubtitleChoice? = null
        val delays = HashMap<String, Double>()

        override suspend fun list(query: SubtitleQuery, refresh: Boolean): List<SubtitleTrack> {
            if (listDelayMs > 0) delay(listDelayMs)
            listCalls++
            if (refresh) refreshCalls++
            return if (lists.size > 1) lists.removeFirst() else lists.firstOrNull().orEmpty()
        }

        override suspend fun cues(id: String, at: Double?): CueFile {
            cueCalls.add(id to at)
            val queue = files[id] ?: return CueFile(emptyList(), status = 429)
            return if (queue.size > 1) queue.removeFirst() else queue.first()
        }

        override suspend fun pretime(id: String) {
            pretimed.add(id)
        }

        override fun choice() = saved
        override fun saveChoice(choice: SubtitleChoice) {
            saved = choice
        }

        override suspend fun savedDelay(hash: String, index: Int) = delays["$hash:$index"] ?: 0.0
        override fun saveDelay(hash: String, index: Int, seconds: Double) {
            delays["$hash:$index"] = seconds
        }
    }

    private fun sub(id: String, lang: String, source: String = "subdl", synced: Boolean = false) =
        SubtitleTrack(id = id, source = source, language = lang, synced = synced)

    private fun file(vararg texts: String, partial: Boolean = false, translating: Boolean = false, sync: String = "synced") =
        CueFile(texts.mapIndexed { i, t -> Cue(i * 5.0, i * 5.0 + 2, t) }, partial = partial, sync = sync, translating = translating)

    private val trQuery = SubtitleQuery(
        tmdbId = 1, type = MediaType.Movie, hash = "abc", index = 0, langs = listOf("tr", "en"), uiLang = "tr", originalLanguage = "en",
    )

    private fun TestScope.controller(backend: FakeBackend, logs: MutableList<Pair<String, Map<String, Any>>> = ArrayList()) =
        SubtitleController(backend, backgroundScope, StandardTestDispatcher(testScheduler)) { testScheduler.currentTime }
            .also { c -> c.log = SubtitleLogSink { e, f -> logs.add(e to f) } }

    @Test
    fun turkishProfilePicksTurkishAndLoadsItsCues() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("en1", "en"), sub("tr1", "tr"), sub("tr2", "tur", synced = true)))
            files["tr2"] = ArrayDeque(listOf(file("Merhaba")))
        }
        val logs = ArrayList<Pair<String, Map<String, Any>>>()
        val c = controller(backend, logs)
        c.onPosition(42.7)
        c.start(trQuery, "eng")
        assertTrue(c.state.value.searching)
        runCurrent()
        val s = c.state.value
        assertFalse(s.searching)
        assertEquals("tr2", s.selectedId)
        assertEquals(listOf("Merhaba"), s.cues.map { it.text })
        assertEquals(listOf("tr1", "en1"), s.menu.map { it.id })
        assertEquals("tr2" to 42.7, backend.cueCalls.single())
        assertEquals("subs", logs.single().first)
        assertEquals("open", logs.single().second["why"])
        // Preferred language there: no retries
        advanceTimeBy(30_000)
        assertEquals(1, backend.listCalls)
    }

    @Test
    fun savedOffShowsNothing() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("tr1", "tr")))
            saved = SubtitleChoice.Off
        }
        val c = controller(backend)
        c.start(trQuery, "en")
        runCurrent()
        assertNull(c.state.value.selectedId)
        assertTrue(backend.cueCalls.isEmpty())
    }

    @Test
    fun earlyRetriesPickTheLanguageWhenItArrives() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("en1", "en")))
            lists.add(listOf(sub("tr1", "tr")))
            files["tr1"] = ArrayDeque(listOf(file("Geç geldi")))
        }
        val c = controller(backend)
        c.start(trQuery, "en")
        runCurrent()
        assertNull(c.state.value.selectedId)
        advanceTimeBy(8_001)
        assertEquals("tr1", c.state.value.selectedId)
        assertEquals(listOf("tr1", "en1"), c.state.value.tracks.map { it.id })
        advanceTimeBy(20_000)
        // The 20 s retry is skipped once the language is there
        assertEquals(2, backend.listCalls)
    }

    @Test
    fun missingLanguageAsksForAnAlternativeAndChecksForLateUploads() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("en1", "en")))
            lists.add(listOf(sub("en1", "en")))
            lists.add(listOf(sub("en1", "en")))
            lists.add(listOf(sub("tr9", "tr", synced = true)))
            files["tr9"] = ArrayDeque(listOf(file("Nihayet")))
        }
        val c = controller(backend)
        val events = ArrayList<SubtitleEvent>()
        backgroundScope.launch(StandardTestDispatcher(testScheduler)) { c.events.toList(events) }
        c.start(trQuery, "en")
        advanceTimeBy(20_001)
        assertEquals(listOf<SubtitleEvent>(SubtitleEvent.Missing("tr")), events)
        advanceTimeBy(SubtitleRepository.LATE_SUBTITLE_POLL_MS)
        assertEquals(1, backend.refreshCalls)
        assertEquals("tr9", c.state.value.selectedId)
        assertEquals(SubtitleEvent.Found("tr"), events.last())
    }

    @Test
    fun turkishAudioNeedsNoTurkishSubtitle() = runTest {
        val backend = FakeBackend().apply { lists.add(listOf(sub("en1", "en"))) }
        val c = controller(backend)
        val events = ArrayList<SubtitleEvent>()
        backgroundScope.launch(StandardTestDispatcher(testScheduler)) { c.events.toList(events) }
        c.start(trQuery, "tur")
        advanceTimeBy(SubtitleRepository.LATE_SUBTITLE_POLL_MS * 2)
        assertTrue(events.isEmpty())
        assertEquals(0, backend.refreshCalls)
    }

    @Test
    fun emptyFileFallsBackToAnotherProvider() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("os1", "tr", source = "opensubtitles", synced = true), sub("os2", "tr", source = "opensubtitles"), sub("sd1", "tr")))
            files["sd1"] = ArrayDeque(listOf(file("Yedek")))
        }
        val logs = ArrayList<Pair<String, Map<String, Any>>>()
        val c = controller(backend, logs)
        c.start(trQuery, "en")
        runCurrent()
        assertEquals("sd1", c.state.value.selectedId)
        assertEquals(listOf("os1", "sd1"), backend.cueCalls.map { it.first })
        assertEquals("fallback", logs.last().second["why"])
    }

    @Test
    fun partialFilesArePolledUntilFinal() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("ai1", "tr", source = "ai")))
            files["ai1"] = ArrayDeque(
                listOf(
                    file("bir", partial = true, translating = true),
                    file("bir", "iki", partial = true),
                    file("bir", "iki", "üç"),
                ),
            )
        }
        val c = controller(backend)
        c.start(trQuery, "en")
        runCurrent()
        assertTrue(c.state.value.translating)
        assertEquals(1, c.state.value.cues.size)
        advanceTimeBy(SubtitleRepository.AI_CUES_POLL_MS + 1)
        assertFalse(c.state.value.translating)
        assertEquals(2, c.state.value.cues.size)
        advanceTimeBy(SubtitleRepository.LIVE_CUES_POLL_MS + 1)
        assertEquals(3, c.state.value.cues.size)
        advanceTimeBy(SubtitleRepository.LIVE_CUES_POLL_MS * 4)
        assertEquals(3, backend.cueCalls.size)
    }

    @Test
    fun anUnusableAiTranslationEndsTheNoteAndIsAskedRarely() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("ai1", "tr", source = "ai")))
            files["ai1"] = ArrayDeque(
                listOf(
                    file("bir", partial = true, translating = true),
                    CueFile(emptyList(), sync = SubsLog.SYNC_NONE, failed = true, status = 502, ai = true),
                ),
            )
        }
        val c = controller(backend)
        c.start(trQuery, "en")
        runCurrent()
        assertTrue(c.state.value.translating)
        advanceTimeBy(SubtitleRepository.AI_CUES_POLL_MS + 1)
        assertFalse(c.state.value.translating)
        assertTrue(c.state.value.aiUnavailable)
        assertEquals(2, backend.cueCalls.size)
        // No 15 s poll: the next ask comes after AI_OFF_RETRY_MS
        advanceTimeBy(SubtitleRules.AI_OFF_RETRY_MS - 1000)
        assertEquals(2, backend.cueCalls.size)
        assertFalse(c.state.value.aiUnavailable)
        advanceTimeBy(2000)
        assertEquals(3, backend.cueCalls.size)
    }

    @Test
    fun aMissingFileAfterEarlierAnswersStopsPolling() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("tr1", "tr"), sub("tr2", "tr")))
            files["tr1"] = ArrayDeque(listOf(file("bir", partial = true), CueFile(emptyList(), status = 404)))
        }
        val c = controller(backend)
        c.start(trQuery, "en")
        runCurrent()
        advanceTimeBy(SubtitleRepository.LIVE_CUES_POLL_MS + 1)
        assertEquals(2, backend.cueCalls.size)
        advanceTimeBy(SubtitleRepository.LIVE_CUES_POLL_MS * 10)
        assertEquals(2, backend.cueCalls.size)
        assertEquals(listOf("bir"), c.state.value.cues.map { it.text })
    }

    @Test
    fun untimedAnswersArePolledEvery5sInTheFirstMinutes() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("tr1", "tr")))
            files["tr1"] = ArrayDeque(listOf(file("bir", partial = true, sync = "pending")))
        }
        val c = controller(backend)
        c.start(trQuery, "en")
        runCurrent()
        assertEquals(1, backend.cueCalls.size)
        advanceTimeBy(SubtitleRepository.PENDING_CUES_POLL_MS + 1)
        assertEquals(2, backend.cueCalls.size)
        // After the first minutes: the regular partial poll
        advanceTimeBy(SubtitleRepository.PENDING_FAST_FOR_MS)
        val n = backend.cueCalls.size
        advanceTimeBy(SubtitleRepository.PENDING_CUES_POLL_MS + 1)
        assertEquals(n, backend.cueCalls.size)
        advanceTimeBy(SubtitleRepository.LIVE_CUES_POLL_MS)
        assertEquals(n + 1, backend.cueCalls.size)
    }

    @Test
    fun theNextEpisodesSubtitleIsPretimedLikeTheOpenWouldPick() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("en2", "en"), sub("tr2", "tr"), sub("tr3", "tr", synced = true)))
        }
        val c = controller(backend)
        c.onAudioLanguage("en")
        c.pretime(trQuery.copy(hash = "def", index = 2))
        runCurrent()
        assertEquals(listOf("tr3"), backend.pretimed)
        // Subtitles off: nothing is timed
        backend.saved = SubtitleChoice.Off
        c.pretime(trQuery.copy(hash = "def", index = 2))
        runCurrent()
        assertEquals(listOf("tr3"), backend.pretimed)
        assertTrue(backend.cueCalls.isEmpty())
    }

    @Test
    fun pretimeStopsWithItsCaller() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("tr3", "tr")))
            listDelayMs = 1000
        }
        val c = controller(backend)
        val job = launch { c.pretime(trQuery.copy(hash = "def", index = 2)) }
        advanceTimeBy(500)
        job.cancel()
        advanceTimeBy(5000)
        assertTrue(backend.pretimed.isEmpty())
    }

    @Test
    fun theSameSubtitleAfterTheBackgroundIsPolledAgain() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("tr1", "tr")))
            files["tr1"] = ArrayDeque(listOf(file("bir", partial = true)))
        }
        val c = controller(backend)
        c.start(trQuery, "en")
        runCurrent()
        assertEquals(1, backend.cueCalls.size)
        c.stop()
        advanceTimeBy(SubtitleRepository.LIVE_CUES_POLL_MS * 2)
        assertEquals(1, backend.cueCalls.size)
        c.restart(trQuery, "en")
        runCurrent()
        assertEquals("tr1", c.state.value.selectedId)
        assertEquals(2, backend.cueCalls.size)
        advanceTimeBy(SubtitleRepository.LIVE_CUES_POLL_MS + 1)
        assertEquals(3, backend.cueCalls.size)
    }

    @Test
    fun aSeekAsksForCuesThatAreNotFinalAtOnce() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("tr1", "tr")))
            files["tr1"] = ArrayDeque(listOf(file("bir", partial = true), file("bir", "iki")))
        }
        val c = controller(backend)
        c.onPosition(10.0)
        c.start(trQuery, "en")
        runCurrent()
        assertEquals(listOf("tr1" to 10.0), backend.cueCalls)
        advanceTimeBy(1000)
        c.onSeek(600.0)
        runCurrent()
        assertEquals("tr1" to 600.0, backend.cueCalls.last())
        assertEquals(2, c.state.value.cues.size)
        // Final now: a seek asks nothing
        c.onSeek(900.0)
        advanceTimeBy(SubtitleRepository.PROVISIONAL_CUES_POLL_MS * 2)
        assertEquals(2, backend.cueCalls.size)
    }

    @Test
    fun provisionalAnswersArePolledEveryMinuteWhilePlayingUntilFinal() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("tr1", "tr")))
            files["tr1"] = ArrayDeque(
                listOf(
                    file("bir", sync = "live"),
                    CueFile(emptyList(), sync = SubsLog.SYNC_NONE, failed = true, status = 0),
                    file("bir", "iki", sync = "live"),
                    file("bir", "iki", "üç", sync = "synced"),
                ),
            )
        }
        val logs = ArrayList<Pair<String, Map<String, Any>>>()
        val c = controller(backend, logs)
        c.start(trQuery, "en")
        runCurrent()
        assertEquals(1, backend.cueCalls.size)
        assertEquals(1, c.state.value.cues.size)
        assertEquals(SubsLog.idHash("tr1"), logs.single().second["id"])
        // Not final (live without the partial mark): asked again a minute later; a failed poll keeps the cues
        advanceTimeBy(SubtitleRepository.PROVISIONAL_CUES_POLL_MS + 1)
        assertEquals(2, backend.cueCalls.size)
        assertEquals(1, c.state.value.cues.size)
        advanceTimeBy(SubtitleRepository.LIVE_CUES_POLL_MS + 1)
        assertEquals(3, backend.cueCalls.size)
        assertEquals(2, c.state.value.cues.size)
        // Paused: no poll until it plays again
        c.onPosition(80.0, playing = false)
        advanceTimeBy(SubtitleRepository.PROVISIONAL_CUES_POLL_MS * 3)
        assertEquals(3, backend.cueCalls.size)
        c.onPosition(80.0, playing = true)
        advanceTimeBy(SubtitleRepository.PROVISIONAL_CUES_POLL_MS + 1)
        assertEquals(4, backend.cueCalls.size)
        assertEquals(3, c.state.value.cues.size)
        assertEquals("synced", c.state.value.sync)
        // Final: not asked again
        advanceTimeBy(SubtitleRepository.PROVISIONAL_CUES_POLL_MS * 5)
        assertEquals(4, backend.cueCalls.size)
    }

    @Test
    fun selectSavesTheChoice() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("en1", "eng"), sub("tr1", "tr")))
            files["en1"] = ArrayDeque(listOf(file("Hello")))
            files["tr1"] = ArrayDeque(listOf(file("Merhaba")))
        }
        val c = controller(backend)
        c.start(trQuery, "en")
        runCurrent()
        c.select("en1")
        runCurrent()
        assertEquals(SubtitleChoice.Language("en", "en1"), backend.saved)
        assertEquals(listOf("Hello"), c.state.value.cues.map { it.text })
        c.select(null)
        assertEquals(SubtitleChoice.Off, backend.saved)
        assertNull(c.state.value.selectedId)
        assertTrue(c.state.value.cues.isEmpty())
    }

    @Test
    fun delayIsClampedLoggedOnceSettledAndKeptPerFile() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("tr1", "tr")))
            files["tr1"] = ArrayDeque(listOf(file("a")))
            delays["abc:0"] = 1.5
        }
        val logs = ArrayList<Pair<String, Map<String, Any>>>()
        val c = controller(backend, logs)
        c.start(trQuery, "en")
        runCurrent()
        assertEquals(1.5, c.state.value.delaySeconds, 0.0)
        assertEquals(2.0, c.setDelay(2.0), 0.0)
        assertEquals(60.0, c.setDelay(99.0), 0.0)
        assertEquals(-0.5, c.setDelay(-0.5), 0.0)
        advanceTimeBy(SubsLog.DELAY_SETTLE_MS + 100)
        val delayEvent = logs.single { it.first == "subdelay" }.second
        assertEquals(-0.5, delayEvent["subdelay"])
        assertEquals(1.5, delayEvent["from"])
        assertEquals(-0.5, backend.delays["abc:0"])
        // Another file: its own delay
        c.restart(trQuery.copy(hash = "def"), "en")
        runCurrent()
        assertEquals(0.0, c.state.value.delaySeconds, 0.0)
    }

    @Test
    fun restartKeepsTheOldSubtitleUntilTheNewListPicks() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("old", "tr")))
            files["old"] = ArrayDeque(listOf(file("eski")))
        }
        val c = controller(backend)
        c.start(trQuery, "en")
        runCurrent()
        assertEquals("old", c.state.value.selectedId)
        backend.lists.clear()
        backend.lists.add(listOf(sub("new", "tr", synced = true)))
        backend.files["new"] = ArrayDeque(listOf(file("yeni")))
        c.restart(trQuery.copy(hash = "def"), "en")
        assertEquals("old", c.state.value.selectedId)
        runCurrent()
        assertEquals("new", c.state.value.selectedId)
        assertEquals(listOf("yeni"), c.state.value.cues.map { it.text })
    }

    @Test
    fun subaltLanguageIsShownAsTheViewersChoice() = runTest {
        val backend = FakeBackend().apply {
            lists.add(listOf(sub("tr1", "tr"), sub("en1", "en")))
            files["en1"] = ArrayDeque(listOf(file("Hi")))
            saved = SubtitleChoice.Off
        }
        val c = controller(backend)
        c.preferLanguage("en")
        c.start(trQuery, "en")
        runCurrent()
        assertEquals("en1", c.state.value.selectedId)
        assertEquals(SubtitleChoice.Language("en"), backend.saved)
    }
}
