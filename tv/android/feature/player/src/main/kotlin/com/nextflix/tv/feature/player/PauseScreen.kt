package com.nextflix.tv.feature.player

import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.tv.material3.Text
import com.nextflix.tv.core.Core
import com.nextflix.tv.core.core
import com.nextflix.tv.core.i18n.Formats
import com.nextflix.tv.core.i18n.LocalAppLanguage
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.SeasonInfo
import com.nextflix.tv.core.model.yearOf
import com.nextflix.tv.core.nav.PlayRequest
import com.nextflix.tv.core.ui.theme.Dimens
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope

/** What the pause screen shows besides the title (webOS lib/catalog.ts PauseInfo). */
private data class PauseInfo(val overview: String, val year: Int?, val runtime: Int, val episodeTitle: String)

/**
 * Netflix-style "You're watching" over the dimmed picture after a while paused (webOS player/PauseScreen.tsx):
 * the title, `S1:E3 "Episode name"` or year and runtime, the overview and "Paused" in the corner. The text is
 * loaded once the screen first shows; a failed load is tried again on the next pause.
 */
@Composable
internal fun BoxScope.PauseScreen(meta: PlayRequest, shown: Boolean) {
    val core = core()
    val t = playerStrings.current()
    val language = LocalAppLanguage.current
    val key = "${meta.type.apiName}|${meta.tmdbId}|${meta.season ?: 0}|${meta.episode ?: 0}|${language.code}"
    var info by remember(key) { mutableStateOf<PauseInfo?>(null) }
    var loaded by remember(key) { mutableStateOf(false) }
    LaunchedEffect(key, shown) {
        if (!shown || loaded) return@LaunchedEffect
        val (result, complete) = pauseInfo(core, meta)
        if (result != null) info = result
        loaded = complete
    }
    val alpha by animateFloatAsState(if (shown) 1f else 0f, tween(800), label = "pause-screen")
    if (alpha == 0f) return

    val isTv = meta.type == MediaType.Tv
    val episodeTitle = meta.episodeTitle?.takeIf { it.isNotEmpty() } ?: info?.episodeTitle.orEmpty()
    val facts = if (isTv) emptyList() else listOfNotNull(info?.year?.toString(), info?.runtime?.takeIf { it > 0 }?.let { Formats.duration(language, it) })
    Box(
        Modifier
            .matchParentSize()
            .alpha(alpha)
            .background(Brush.horizontalGradient(0f to Color.Black.copy(alpha = 0.88f), 0.45f to Color.Black.copy(alpha = 0.6f), 1f to Color.Black.copy(alpha = 0.2f))),
    ) {
        Column(Modifier.align(Alignment.CenterStart).padding(start = Dimens.ScreenPadH).width(496.dp)) {
            Text(t.youreWatching, style = NfType.Body.copy(fontSize = 16.sp), color = NfColors.Muted)
            Text(
                meta.title,
                modifier = Modifier.padding(top = 6.dp),
                style = NfType.Display.copy(fontSize = 45.sp, lineHeight = 49.sp),
                maxLines = 2,
                overflow = TextOverflow.Ellipsis,
            )
            if (isTv && meta.season != null && meta.episode != null) {
                Text(
                    buildAnnotatedString {
                        append(Formats.episode(language, meta.season, meta.episode))
                        if (episodeTitle.isNotEmpty()) withStyle(SpanStyle(fontWeight = FontWeight.Normal)) { append(" “$episodeTitle”") }
                    },
                    modifier = Modifier.padding(top = 18.dp),
                    style = NfType.Body.copy(fontSize = 19.sp, fontWeight = FontWeight.SemiBold),
                )
            }
            if (facts.isNotEmpty()) {
                Text(facts.joinToString(" · "), modifier = Modifier.padding(top = 14.dp), style = NfType.Body.copy(fontSize = 16.sp), color = NfColors.Muted)
            }
            info?.overview?.takeIf { it.isNotEmpty() }?.let {
                Text(
                    it,
                    modifier = Modifier.padding(top = 14.dp),
                    style = NfType.Body.copy(fontSize = 16.sp, lineHeight = 23.sp),
                    color = Color(0xFFD2D2D2),
                    maxLines = 4,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }
        Text(
            t.paused,
            modifier = Modifier.align(Alignment.BottomEnd).padding(end = Dimens.ScreenPadH, bottom = 56.dp),
            style = NfType.Body.copy(fontSize = 19.sp),
            color = NfColors.Muted,
        )
    }
}

/** The episode's overview (the show's when TMDB has none) or the film's; complete = nothing failed. */
private suspend fun pauseInfo(core: Core, m: PlayRequest): Pair<PauseInfo?, Boolean> = coroutineScope {
    if (m.type == MediaType.Tv && m.season != null && m.episode != null) {
        val eps = async { quiet { core.catalog.loadSeasonEpisodes(m.tmdbId, SeasonInfo(m.season!!, 0)) } }
        val details = async { quiet { core.catalog.details(m.tmdbId, MediaType.Tv) } }
        val episodes = eps.await()
        val ep = episodes?.firstOrNull { it.episodeNumber == m.episode }
        val d = details.await()
        if (ep?.overview.isNullOrEmpty() && d == null) return@coroutineScope null to false
        PauseInfo(
            overview = ep?.overview?.takeIf { it.isNotEmpty() } ?: d?.overview.orEmpty(),
            year = d?.let { yearOf(it.date) },
            runtime = ep?.runtime?.takeIf { it > 0 } ?: d?.episodeRunTime ?: 0,
            episodeTitle = ep?.takeIf { it.hasRealName }?.name.orEmpty(),
        ) to (episodes != null && d != null)
    } else {
        val d = quiet { core.catalog.details(m.tmdbId, m.type) } ?: return@coroutineScope null to false
        PauseInfo(d.overview, yearOf(d.date), d.runtime, "") to true
    }
}
