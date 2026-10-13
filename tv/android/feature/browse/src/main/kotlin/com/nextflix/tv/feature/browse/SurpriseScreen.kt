package com.nextflix.tv.feature.browse

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.em
import androidx.compose.ui.unit.sp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.tv.material3.Icon
import androidx.tv.material3.Text
import coil3.compose.AsyncImage
import com.nextflix.tv.core.Core
import com.nextflix.tv.core.LocalCore
import com.nextflix.tv.core.i18n.CoreStrings
import com.nextflix.tv.core.i18n.LocalAppLanguage
import com.nextflix.tv.core.i18n.strings
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.nav.Navigator
import com.nextflix.tv.core.nav.Route
import com.nextflix.tv.core.net.TmdbImages
import com.nextflix.tv.core.ui.components.ButtonStyle
import com.nextflix.tv.core.ui.components.NfButton
import com.nextflix.tv.core.ui.components.Spinner
import com.nextflix.tv.core.ui.focus.FocusRestore
import com.nextflix.tv.core.ui.focus.restorableFocus
import com.nextflix.tv.core.ui.focus.tryFocus
import com.nextflix.tv.core.ui.icons.Lucide
import com.nextflix.tv.core.ui.theme.Dimens
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlin.time.Duration.Companion.hours

@Immutable
internal data class SurpriseState(val pick: SurprisePick? = null, val loading: Boolean = false, val failed: Boolean = false)

/** One pick per press (GET /api/recommendations/surprise); kept while the screen lives, so Back from Detail shows it again. */
internal class SurpriseViewModel(private val core: Core) : ViewModel() {
    private val _state = MutableStateFlow(SurpriseState())
    val state: StateFlow<SurpriseState> = _state
    private var job: Job? = null

    init {
        next()
    }

    fun next() {
        if (job?.isActive == true) return
        _state.update { it.copy(loading = true, failed = false) }
        job = viewModelScope.launch {
            val pick = try {
                fetch()
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) {
                null
            }
            _state.update { if (pick != null) SurpriseState(pick) else it.copy(loading = false, failed = true) }
        }
    }

    private suspend fun fetch(): SurprisePick? {
        val pid = core.session.profileId ?: return null
        // The last picks of this session go along as exclude= (the server also remembers 30 per profile)
        val key = core.cache.scoped("browse-surprise-recent")
        val recent = core.cache.peek<List<String>>(key).orEmpty()
        val query = buildMap<String, Any?> {
            put("profile_id", pid)
            if (recent.isNotEmpty()) put("exclude", recent.joinToString(","))
        }
        val pick = core.api.getJson("/api/recommendations/surprise", query).toSurprisePick() ?: return null
        core.cache.store(key, 12.hours, (recent + pick.title.key).takeLast(RECENT_MAX))
        return pick
    }

    private companion object {
        const val RECENT_MAX = 30
    }
}

/** Match, year, type and up to three genres. */
internal fun surpriseFacts(pick: SurprisePick, s: CoreStrings): List<String> = buildList {
    val t = pick.title
    t.matchPercent.takeIf { it > 0 }?.let { add(s.match(it)) }
    t.year?.let { add(it.toString()) }
    add(if (t.type == MediaType.Tv) s.tvShows else s.movies)
    val genres = t.genreIds.map(s::genre).filter { it.isNotEmpty() }.take(3)
    if (genres.isNotEmpty()) add(genres.joinToString(" • "))
}

private const val KEY_PLAY = "surprise-play"
private const val KEY_RETRY = "surprise-retry"

/** "Surprise me" (webOS screens/SurpriseScreen.tsx): one title full screen, with the reason it was picked. */
@Composable
internal fun SurpriseRoute(nav: Navigator) {
    val core = LocalCore.current
    val vm = viewModel { SurpriseViewModel(core) }
    val state by vm.state.collectAsStateWithLifecycle()
    val s = strings()
    val bs = browseStrings.current()
    val language = LocalAppLanguage.current
    val pick = state.pick
    val playFocus = remember { FocusRequester() }
    val retryFocus = remember { FocusRequester() }
    var hasFocus by remember { mutableStateOf(false) }

    FocusRestore(ready = pick != null || state.failed, fallbackKey = if (pick != null) KEY_PLAY else KEY_RETRY)
    // Retry hides its button while it loads: focus the next one once it is there
    LaunchedEffect(pick != null, state.failed) {
        repeat(2) { withFrameNanos { } }
        if (!hasFocus) {
            when {
                pick != null -> playFocus.tryFocus()
                state.failed -> retryFocus.tryFocus()
            }
        }
    }

    Box(
        Modifier
            .fillMaxSize()
            .background(Color.Black)
            .onFocusChanged { hasFocus = it.hasFocus },
    ) {
        HeroImage(
            pick?.title?.backdrop.orEmpty(),
            Modifier.align(Alignment.TopEnd).fillMaxWidth(0.78f).fillMaxHeight(),
            size = TmdbImages.W1280,
        )
        Box(
            Modifier
                .fillMaxSize()
                .background(
                    Brush.horizontalGradient(
                        0.22f to Color.Black,
                        0.38f to Color.Black.copy(alpha = 0.85f),
                        0.70f to Color.Black.copy(alpha = 0.2f),
                        1f to Color.Transparent,
                    ),
                ),
        )
        Box(Modifier.fillMaxSize().background(Brush.verticalGradient(0.65f to Color.Transparent, 1f to Color.Black.copy(alpha = 0.7f))))

        Column(
            Modifier
                .fillMaxHeight()
                .padding(start = Dimens.ScreenPadH, bottom = 112.dp)
                .width(496.dp),
            verticalArrangement = Arrangement.Bottom,
        ) {
            Text(
                bs.surpriseHeading.uppercase(language.locale),
                style = NfType.Caption.copy(fontSize = 12.sp, fontWeight = FontWeight.Bold, letterSpacing = 0.3.em),
                color = NfColors.Muted,
                modifier = Modifier.padding(bottom = 16.dp),
            )
            when {
                pick != null -> PickBody(
                    pick = pick,
                    reason = reasonText(bs, language, pick),
                    facts = surpriseFacts(pick, s),
                    playFocus = playFocus,
                    onPlay = { nav.push(Route.Detail(pick.title.type, pick.title.id, autoplay = true)) },
                    onDetails = { nav.push(Route.Detail(pick.title.type, pick.title.id)) },
                    onNext = vm::next,
                )
                state.failed -> {
                    Text(bs.surpriseError, style = NfType.Body.copy(fontSize = 16.sp), color = NfColors.Muted)
                    Spacer(Modifier.size(24.dp))
                    NfButton(
                        s.retry,
                        onClick = vm::next,
                        modifier = Modifier.restorableFocus(KEY_RETRY).focusRequester(retryFocus),
                    )
                }
                else -> Spinner()
            }
        }
        if (pick != null && state.loading) {
            Spinner(Modifier.align(Alignment.CenterStart).offset(x = Dimens.ScreenPadH + 496.dp + 56.dp))
        }
    }
}

@Composable
private fun PickBody(
    pick: SurprisePick,
    reason: String,
    facts: List<String>,
    playFocus: FocusRequester,
    onPlay: () -> Unit,
    onDetails: () -> Unit,
    onNext: () -> Unit,
) {
    val core = LocalCore.current
    val s = strings()
    val bs = browseStrings.current()
    val title = pick.title
    var logoFailed by remember(pick.logo) { mutableStateOf(false) }

    if (pick.logo.isNotEmpty() && !logoFailed) {
        AsyncImage(
            model = core.imageUrl(pick.logo, TmdbImages.W500),
            contentDescription = title.title,
            contentScale = ContentScale.Fit,
            alignment = Alignment.BottomStart,
            onError = { logoFailed = true },
            modifier = Modifier.padding(bottom = 16.dp).size(352.dp, 112.dp),
        )
    } else {
        Text(
            title.title,
            style = NfType.Display.copy(fontSize = 40.sp, lineHeight = 44.sp),
            maxLines = 3,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(bottom = 16.dp),
        )
    }
    Row(verticalAlignment = Alignment.CenterVertically) {
        Icon(Lucide.Shuffle, contentDescription = null, tint = NfColors.Red, modifier = Modifier.size(18.dp))
        Spacer(Modifier.width(8.dp))
        Text(reason, style = NfType.Body.copy(fontSize = 16.sp, fontWeight = FontWeight.SemiBold), color = Color.White)
    }
    Facts(facts, Modifier.padding(top = 10.dp))
    if (title.overview.isNotEmpty()) {
        Text(
            title.overview,
            style = NfType.Body.copy(fontSize = 13.6.sp, lineHeight = 20.4.sp),
            color = NfColors.TextSoft,
            maxLines = 4,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(top = 13.dp),
        )
    }
    Row(Modifier.padding(top = 24.dp), horizontalArrangement = Arrangement.spacedBy(11.dp)) {
        NfButton(
            s.play,
            onClick = onPlay,
            modifier = Modifier.restorableFocus(KEY_PLAY).focusRequester(playFocus),
            icon = PlayFilled,
        )
        NfButton(
            bs.surpriseDetails,
            onClick = onDetails,
            modifier = Modifier.restorableFocus("surprise-details"),
            icon = Lucide.Info,
            style = ButtonStyle.Secondary,
        )
        NfButton(
            bs.surpriseNext,
            onClick = onNext,
            modifier = Modifier.restorableFocus("surprise-next"),
            icon = Lucide.Shuffle,
            style = ButtonStyle.Secondary,
        )
    }
}
