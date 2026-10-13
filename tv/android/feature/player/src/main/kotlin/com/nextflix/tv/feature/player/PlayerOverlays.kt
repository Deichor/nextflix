package com.nextflix.tv.feature.player

import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.focusable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.scale
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusProperties
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.key
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.em
import androidx.compose.ui.unit.sp
import androidx.tv.material3.ClickableSurfaceDefaults
import androidx.tv.material3.Icon
import androidx.tv.material3.Surface
import androidx.tv.material3.Text
import com.nextflix.tv.core.core
import com.nextflix.tv.core.i18n.Formats
import com.nextflix.tv.core.i18n.LocalAppLanguage
import com.nextflix.tv.core.i18n.strings
import com.nextflix.tv.core.model.AvailabilityStatus
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.net.TmdbImages
import com.nextflix.tv.core.ui.components.ButtonStyle
import com.nextflix.tv.core.ui.components.CardImage
import com.nextflix.tv.core.ui.components.NfButton
import com.nextflix.tv.core.ui.components.Spinner
import com.nextflix.tv.core.ui.focus.isSelect
import com.nextflix.tv.core.ui.focus.onKeyDown
import com.nextflix.tv.core.ui.focus.repeatCount
import com.nextflix.tv.core.ui.focus.tryFocus
import com.nextflix.tv.core.ui.icons.Lucide
import com.nextflix.tv.core.ui.icons.filled
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType
import com.nextflix.tv.feature.subtitles.SubtitleState
import kotlinx.coroutines.delay

// The player's layers above the video (webOS Player.tsx markup and styles.css: 1 rem = 8 dp).

private val Muted = NfColors.Muted
private val Bolt by lazy { Lucide.Zap.filled() }
private val PlayFilled by lazy { Lucide.Play.filled() }
private val SkipFilled by lazy { Lucide.SkipForward.filled() }

@Composable
internal fun BoxScope.PlayerOverlays(
    engine: PlayerEngine,
    s: PlayerState,
    subs: SubtitleState,
    ui: PlayerUi,
    overlay: Overlay?,
    controls: Boolean,
    skipShown: SkipPhase?,
    skipSegs: SkipSegments?,
    onClose: () -> Unit,
) {
    val t = playerStrings.current()
    val language = LocalAppLanguage.current
    val m = s.meta
    val subtitle = if (m.type == MediaType.Tv) {
        listOfNotNull(Formats.episode(language, m.season, m.episode).ifEmpty { null }, m.episodeTitle?.ifEmpty { null }).joinToString(" · ")
    } else {
        ""
    }

    if (overlay == Overlay.Resolving || overlay == Overlay.Error || overlay == Overlay.Unreleased) {
        Cover(engine, s, overlay, subtitle, ui.overlayFocus, onClose)
    }

    if (overlay == null && s.phase == Phase.Ready && s.buffering && !s.ended) {
        Column(Modifier.align(Alignment.Center), horizontalAlignment = Alignment.CenterHorizontally) {
            Spinner()
            Status(t.run { if (s.reconnecting) reconnecting else msg(if (s.duration > 0) Msg.Buffering else s.status) })
            if (s.holding && !s.reconnecting) StatusSub(t.smoothBuffering)
        }
    }

    if (overlay == Overlay.Fatal) {
        Dialog(Modifier.align(Alignment.Center)) {
            Icon(Lucide.CircleAlert, null, Modifier.size(40.dp), tint = NfColors.Red)
            Status(errorText(s.fatal))
            DialogButtons {
                NfButton(t.retry, engine::retry, Modifier.focusRequester(ui.overlayFocus))
                NfButton(strings().close, onClose, style = ButtonStyle.Secondary)
            }
        }
    }

    if (overlay == Overlay.Countdown) {
        val next = s.next
        val label = next?.let { Formats.episode(language, it.first, it.second) }.orEmpty()
        Dialog(Modifier.align(Alignment.Center)) {
            Text(t.nextEpisode.uppercase(language.locale), style = NfType.Caption.copy(fontSize = 11.sp, letterSpacing = 0.25.em), color = Muted)
            Status(t.nextStartsIn(label, maxOf(0, s.countdown ?: 0)))
            DialogButtons {
                NfButton(t.playNow, { engine.playNext() }, Modifier.focusRequester(ui.overlayFocus), icon = PlayFilled)
                NfButton(strings().cancel, engine::stopCountdown, style = ButtonStyle.Secondary)
            }
        }
    }

    if (skipShown != null && !ui.panel) {
        val bottom by animateDpAsState(if (controls) 176.dp else 72.dp, tween(300), label = "skip")
        Row(Modifier.align(Alignment.BottomEnd).padding(end = 40.dp, bottom = bottom), verticalAlignment = Alignment.CenterVertically) {
            if (skipShown == SkipPhase.Intro) {
                SkipButton(t.skipIntro, Lucide.ChevronsRight, ui, engine) {
                    skipSegs?.intro?.let { engine.seekTo(it.end, "skip-intro") }
                }
            } else {
                if (s.countdown != null) {
                    NfButton(
                        t.watchCredits,
                        {
                            ui.creditsDismissed = true
                            engine.stopCountdown()
                            ui.skipFocus.tryFocus()
                        },
                        Modifier.onFocusChanged { if (it.isFocused) ui.zone = Zone.Skip },
                        style = ButtonStyle.Secondary,
                    )
                    Spacer(Modifier.width(8.dp))
                }
                NextFillButton(t.nextEpisode, s.countdown, ui, engine) { engine.playNext(fromCredits = true) }
            }
        }
    }

    val toast = s.toast
    when {
        toast != null -> Toast(t.msg(toast))
        subs.translating -> Toast(t.aiTranslating, spinner = true)
    }

    TopBar(s, subtitle, visible = (controls || overlay != null) && !ui.styling, showHeading = overlay == null, ui = ui, onClose = onClose)

    if (s.playable) BottomControls(engine, s, ui, controls && !ui.styling)
}

@Composable
private fun BoxScope.Cover(engine: PlayerEngine, s: PlayerState, overlay: Overlay, subtitle: String, focus: FocusRequester, onClose: () -> Unit) {
    val t = playerStrings.current()
    val core = core()
    val language = LocalAppLanguage.current
    val m = s.meta
    Box(Modifier.matchParentSize().background(Color.Black)) {
        val url = core.imageUrl(m.backdropPath, TmdbImages.W1280)
        if (url.isNotEmpty()) CardImage(url, Modifier.fillMaxSize().alpha(0.25f), contentScale = ContentScale.Crop)
        Box(
            Modifier.fillMaxSize().background(
                Brush.verticalGradient(0f to Color.Black.copy(alpha = 0.4f), 0.5f to Color.Black.copy(alpha = 0.6f), 1f to Color.Black),
            ),
        )
        Column(
            Modifier.align(Alignment.Center).widthIn(max = 640.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Text(m.title, style = NfType.Display.copy(fontSize = 32.sp, lineHeight = 36.sp), textAlign = TextAlign.Center, maxLines = 2)
            if (subtitle.isNotEmpty()) {
                Spacer(Modifier.height(5.dp))
                Text(subtitle, style = NfType.Body.copy(fontSize = 14.sp), color = Muted)
            }
            when (overlay) {
                Overlay.Resolving -> if (s.resolveElapsedMs >= 300) {
                    Spacer(Modifier.height(16.dp))
                    Spinner()
                    Status(t.msg(s.stage))
                    StatusSub(if (s.resolveElapsedMs >= 30_000) t.stillSearching else "")
                }
                Overlay.Error -> {
                    Icon(Lucide.CircleAlert, null, Modifier.padding(top = 16.dp).size(40.dp), tint = NfColors.Red)
                    Status(if (m.type == MediaType.Tv) t.couldNotStartEpisode else t.couldNotStartTitle)
                    StatusSub(errorText(s.error))
                    DialogButtons {
                        NfButton(t.retry, engine::retry, Modifier.focusRequester(focus))
                        NfButton(strings().close, onClose, style = ButtonStyle.Secondary)
                    }
                }
                Overlay.Unreleased -> {
                    val c = strings()
                    val a = s.unreleased
                    Icon(Lucide.CalendarClock, null, Modifier.padding(top = 16.dp).size(40.dp), tint = NfColors.Red)
                    Status(if (m.type == MediaType.Tv) c.episodeNotReleased else c.titleNotReleased)
                    val date = if (a?.status == AvailabilityStatus.InTheaters) a.digitalDate else a?.releaseDate ?: a?.digitalDate
                    if (!date.isNullOrEmpty()) {
                        val d = Formats.date(language, date)
                        StatusSub(if (a?.status == AvailabilityStatus.InTheaters) c.inTheatersUntil(d) else c.comingOn(d))
                    }
                    DialogButtons {
                        NfButton(c.close, onClose, Modifier.focusRequester(focus))
                    }
                }
                else -> Unit
            }
        }
    }
}

@Composable
private fun errorText(e: PlayerError?): String = when (e) {
    null -> ""
    is PlayerError.Key -> playerStrings.current().msg(e.msg)
    is PlayerError.Text -> e.text
}

@Composable
private fun Status(text: String) {
    Text(
        text,
        modifier = Modifier.padding(top = 8.dp),
        style = NfType.Body.copy(fontSize = 16.sp, fontWeight = FontWeight.SemiBold),
        textAlign = TextAlign.Center,
    )
}

@Composable
private fun StatusSub(text: String) {
    Text(
        text,
        modifier = Modifier.padding(top = 5.dp).heightIn(min = 18.dp),
        style = NfType.Body.copy(fontSize = 13.sp),
        color = Muted,
        textAlign = TextAlign.Center,
    )
}

@Composable
private fun Dialog(modifier: Modifier, content: @Composable () -> Unit) {
    Column(
        modifier
            .widthIn(min = 480.dp)
            .background(Color(0xEB141414), RoundedCornerShape(6.dp))
            .padding(horizontal = 32.dp, vertical = 24.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) { content() }
}

@Composable
private fun DialogButtons(content: @Composable () -> Unit) {
    Row(Modifier.padding(top = 21.dp), horizontalArrangement = Arrangement.spacedBy(12.dp)) { content() }
}

@Composable
private fun BoxScope.Toast(text: String, spinner: Boolean = false) {
    Row(
        Modifier
            .align(Alignment.TopCenter)
            .padding(top = 72.dp)
            .background(Color(0xCC000000), RoundedCornerShape(3.dp))
            .border(1.dp, Color.White.copy(alpha = 0.25f), RoundedCornerShape(3.dp))
            .padding(horizontal = 16.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (spinner) {
            Spinner(size = 14.dp)
            Spacer(Modifier.width(8.dp))
        }
        Text(text, style = NfType.Body.copy(fontSize = 13.sp))
    }
}

@Composable
private fun SkipButton(text: String, icon: ImageVector, ui: PlayerUi, engine: PlayerEngine, onClick: () -> Unit) {
    Surface(
        onClick = onClick,
        modifier = Modifier
            .height(35.dp)
            .focusRequester(ui.skipFocus)
            .onFocusChanged { if (it.isFocused) ui.zone = Zone.Skip }
            .onKeyDown { e -> skipKey(e, ui, engine) },
        shape = ClickableSurfaceDefaults.shape(RoundedCornerShape(4.dp)),
        colors = ClickableSurfaceDefaults.colors(
            containerColor = Color(0x99000000),
            contentColor = Color.White,
            focusedContainerColor = Color.White,
            focusedContentColor = Color.Black,
        ),
        border = ClickableSurfaceDefaults.border(
            border = androidx.tv.material3.Border(BorderStroke(1.6.dp, Color.White.copy(alpha = 0.85f)), shape = RoundedCornerShape(4.dp)),
        ),
        scale = ClickableSurfaceDefaults.scale(focusedScale = 1.06f),
    ) {
        Row(Modifier.align(Alignment.Center).padding(horizontal = 20.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(icon, null, Modifier.size(17.dp))
            Spacer(Modifier.width(7.dp))
            Text(text, style = NfType.Button)
        }
    }
}

/** "Next episode" that fills up while the 5 s countdown runs. */
@Composable
private fun NextFillButton(text: String, countdown: Int?, ui: PlayerUi, engine: PlayerEngine, onClick: () -> Unit) {
    val fill by animateFloatAsState(if (countdown != null) (5 - countdown) / 5f else 0f, tween(1000), label = "next-fill")
    Surface(
        onClick = onClick,
        modifier = Modifier
            .height(35.dp)
            .focusRequester(ui.skipFocus)
            .onFocusChanged { if (it.isFocused) ui.zone = Zone.Skip }
            .onKeyDown { e -> skipKey(e, ui, engine) },
        shape = ClickableSurfaceDefaults.shape(RoundedCornerShape(4.dp)),
        colors = ClickableSurfaceDefaults.colors(
            containerColor = Color.White.copy(alpha = 0.55f),
            contentColor = Color.Black,
            focusedContainerColor = Color.White.copy(alpha = 0.7f),
            focusedContentColor = Color.Black,
        ),
        scale = ClickableSurfaceDefaults.scale(focusedScale = 1.06f),
    ) {
        if (countdown != null) Box(Modifier.fillMaxHeight().fillMaxWidth(fill).background(Color.White))
        Row(Modifier.align(Alignment.Center).padding(horizontal = 20.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(SkipFilled, null, Modifier.size(17.dp))
            Spacer(Modifier.width(7.dp))
            Text(text, style = NfType.Button)
        }
    }
}

/** On a lone Skip button Left / Right still seek. */
private fun skipKey(e: androidx.compose.ui.input.key.KeyEvent, ui: PlayerUi, engine: PlayerEngine): Boolean {
    val st = engine.state.value
    if ((e.key == Key.DirectionLeft || e.key == Key.DirectionRight) && st.playable && st.countdown == null) {
        ui.timelineFocus.tryFocus()
        ui.scrub(if (e.key == Key.DirectionRight) 1 else -1, e.repeatCount > 0)
        return true
    }
    return false
}

@Composable
private fun BoxScope.TopBar(s: PlayerState, subtitle: String, visible: Boolean, showHeading: Boolean, ui: PlayerUi, onClose: () -> Unit) {
    val t = playerStrings.current()
    val alpha by animateFloatAsState(if (visible) 1f else 0f, tween(300), label = "top")
    Row(
        Modifier
            .align(Alignment.TopStart)
            .fillMaxWidth()
            .alpha(alpha)
            .background(Brush.verticalGradient(listOf(Color.Black.copy(alpha = 0.8f), Color.Transparent)))
            .padding(start = 32.dp, end = 32.dp, top = 24.dp, bottom = 48.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Surface(
            onClick = onClose,
            modifier = Modifier.size(40.dp).focusRequester(ui.backFocus).focusProperties { canFocus = visible && !ui.panel },
            shape = ClickableSurfaceDefaults.shape(CircleShape),
            colors = ClickableSurfaceDefaults.colors(
                containerColor = Color.Transparent,
                contentColor = Color.White,
                focusedContainerColor = Color.White,
                focusedContentColor = Color.Black,
            ),
        ) {
            Icon(Lucide.ArrowLeft, t.back, Modifier.align(Alignment.Center).size(26.dp))
        }
        Spacer(Modifier.width(16.dp))
        Column(Modifier.weight(1f)) {
            if (showHeading) {
                Text(s.meta.title, style = NfType.Body.copy(fontSize = 19.sp, fontWeight = FontWeight.SemiBold), maxLines = 1, overflow = TextOverflow.Ellipsis)
                if (subtitle.isNotEmpty()) Text(subtitle, style = NfType.Body.copy(fontSize = 13.sp), color = Muted, maxLines = 1)
            }
        }
        val q = s.quality
        if (q != null && showHeading) {
            Badge(q.label)
            if (q.dv) Badge("Dolby Vision") else if (q.hdr) Badge("HDR")
            if (s.audioBadge.isNotEmpty()) Badge(s.audioBadge)
            if (s.debrid) Badge("TorBox", Bolt)
            if (s.cinema) Badge(t.cinemaBadge, Lucide.Clapperboard)
        }
    }
}

@Composable
private fun Badge(text: String, icon: ImageVector? = null) {
    Row(
        Modifier
            .padding(start = 6.dp)
            .border(1.2.dp, Color.White.copy(alpha = 0.6f), RoundedCornerShape(2.dp))
            .padding(horizontal = 6.dp, vertical = 2.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (icon != null) {
            Icon(icon, null, Modifier.size(12.dp), tint = NfColors.TextSoft)
            Spacer(Modifier.width(3.dp))
        }
        Text(text, style = NfType.Caption.copy(fontSize = 10.sp, fontWeight = FontWeight.ExtraBold, letterSpacing = 0.05.em))
    }
}

@Composable
private fun BoxScope.BottomControls(engine: PlayerEngine, s: PlayerState, ui: PlayerUi, visible: Boolean) {
    val t = playerStrings.current()
    val language = LocalAppLanguage.current
    val alpha by animateFloatAsState(if (visible) 1f else 0f, tween(300), label = "bottom")
    val preview = ui.preview
    val scrubbing = preview != null && s.file != null
    val shown = preview ?: s.position
    val fraction = if (s.duration > 0) (shown / s.duration).toFloat().coerceIn(0f, 1f) else 0f
    val focusable = !ui.panel

    Column(
        Modifier
            .align(Alignment.BottomStart)
            .fillMaxWidth()
            .alpha(alpha)
            .background(Brush.verticalGradient(listOf(Color.Transparent, Color.Black.copy(alpha = 0.9f))))
            .padding(start = 40.dp, end = 40.dp, top = if (scrubbing) 272.dp else 64.dp, bottom = 28.dp),
    ) {
        Row(
            Modifier
                .padding(bottom = 19.dp)
                .alpha(if (scrubbing) 0f else 1f)
                .onKeyDown { e ->
                    if (e.key == Key.DirectionDown) {
                        ui.timelineFocus.tryFocus()
                        true
                    } else {
                        false
                    }
                },
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            PillButton(if (s.playing) Lucide.Pause else PlayFilled, if (s.playing) t.pause else t.play, ui.playPauseFocus, ui, focusable) { engine.togglePlay() }
            PillButton(Lucide.RotateCcw, t.back10, null, ui, focusable) { engine.seekTo(engine.positionNow() - 10) }
            PillButton(Lucide.RotateCw, t.forward10, null, ui, focusable) { engine.seekTo(engine.positionNow() + 10) }
            s.next?.let { (season, episode) ->
                PillButton(Lucide.SkipForward, "${t.nextEpisode} ${Formats.episode(language, season, episode)}", null, ui, focusable) { engine.playNext() }
            }
            PillButton(Lucide.MessageSquare, t.audioSubtitles, ui.audioFocus, ui, focusable) { ui.openPanel() }
        }
        Timeline(engine, s, ui, fraction, shown, scrubbing, focusable)
    }

    val file = s.file
    if (preview != null && file != null) {
        TrickPlay(engine.thumbs, file.hash, file.index, preview, s.duration, s.aspect, Modifier.align(Alignment.BottomCenter).padding(bottom = 80.dp))
        if (ui.scrubMode) {
            Text(
                t.scrubHint,
                modifier = Modifier.align(Alignment.BottomCenter).padding(bottom = 30.dp),
                style = NfType.Body.copy(fontSize = 13.sp),
                color = Color.White.copy(alpha = 0.8f),
            )
        }
    }
}

/** A round control button that shows its label while focused (webOS .pbtn). */
@Composable
private fun PillButton(icon: ImageVector, label: String, requester: FocusRequester?, ui: PlayerUi, canFocus: Boolean, onClick: () -> Unit) {
    var focused by remember { mutableStateOf(false) }
    Surface(
        onClick = onClick,
        modifier = Modifier
            .height(37.dp)
            .then(if (requester != null) Modifier.focusRequester(requester) else Modifier)
            .focusProperties { this.canFocus = canFocus }
            .onFocusChanged {
                focused = it.isFocused
                if (it.isFocused) ui.zone = Zone.Buttons
            },
        shape = ClickableSurfaceDefaults.shape(RoundedCornerShape(50)),
        colors = ClickableSurfaceDefaults.colors(
            containerColor = Color.Transparent,
            contentColor = Color.White,
            focusedContainerColor = Color.White,
            focusedContentColor = Color.Black,
        ),
        scale = ClickableSurfaceDefaults.scale(focusedScale = 1f),
    ) {
        Row(Modifier.align(Alignment.Center).padding(horizontal = 11.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(icon, label, Modifier.size(19.dp))
            if (focused) {
                Spacer(Modifier.width(8.dp))
                Text(label, style = NfType.Button.copy(fontSize = 12.sp), maxLines = 1)
            }
        }
    }
}

@Composable
private fun Timeline(engine: PlayerEngine, s: PlayerState, ui: PlayerUi, fraction: Float, shown: Double, scrubbing: Boolean, canFocus: Boolean) {
    var focused by remember { mutableStateOf(false) }
    val barHeight by animateDpAsState(if (focused) 6.dp else 5.dp, label = "bar")
    val thumbScale by animateFloatAsState(if (focused) 1.6f else 1f, tween(150), label = "thumb")
    Column(
        Modifier
            .fillMaxWidth()
            .focusRequester(ui.timelineFocus)
            .focusProperties { this.canFocus = canFocus }
            .onFocusChanged {
                focused = it.isFocused
                if (it.isFocused) ui.zone = Zone.Timeline
            }
            .onKeyDown { e -> timelineKey(e, ui, engine) }
            .focusable()
            .padding(vertical = 10.dp),
    ) {
        Box(Modifier.fillMaxWidth().height(14.dp), contentAlignment = Alignment.CenterStart) {
            Box(Modifier.fillMaxWidth().height(barHeight).clip(RoundedCornerShape(3.dp)).background(Color.White.copy(alpha = 0.3f)))
            Box(Modifier.fillMaxWidth(fraction).height(barHeight).clip(RoundedCornerShape(3.dp)).background(NfColors.Red))
            BarThumb(fraction, thumbScale, if (focused) Color.White else NfColors.Red)
            val preview = ui.preview
            if (preview != null && !scrubbing) PreviewLabel(fraction, formatTime(preview))
        }
        Row(Modifier.fillMaxWidth().padding(top = 8.dp), horizontalArrangement = Arrangement.SpaceBetween) {
            Text(formatTime(shown), style = NfType.Body.copy(fontSize = 12.sp), color = NfColors.TextSoft)
            Text("-" + formatTime(maxOf(0.0, s.duration - shown)), style = NfType.Body.copy(fontSize = 12.sp), color = NfColors.TextSoft)
        }
    }
}

@Composable
private fun BoxScope.BarThumb(fraction: Float, scale: Float, color: Color) {
    Box(Modifier.matchParentSize()) {
        Box(
            Modifier
                .fillMaxWidth(fraction)
                .align(Alignment.CenterStart),
        ) {
            Box(
                Modifier
                    .align(Alignment.CenterEnd)
                    .offset(x = 5.5.dp)
                    .size(11.dp)
                    .scale(scale)
                    .background(color, CircleShape),
            )
        }
    }
}

@Composable
private fun BoxScope.PreviewLabel(fraction: Float, text: String) {
    Box(Modifier.matchParentSize()) {
        Box(Modifier.fillMaxWidth(fraction).align(Alignment.CenterStart)) {
            Text(
                text,
                modifier = Modifier
                    .align(Alignment.CenterEnd)
                    .offset(x = 24.dp, y = (-26).dp)
                    .background(Color(0xD9000000), RoundedCornerShape(2.dp))
                    .padding(horizontal = 8.dp, vertical = 3.dp),
                style = NfType.Body.copy(fontSize = 14.sp, fontWeight = FontWeight.SemiBold),
            )
        }
    }
}

/** The timeline zone: Left / Right seek, Up to the buttons, OK play / pause or seek to the preview. */
private fun timelineKey(e: androidx.compose.ui.input.key.KeyEvent, ui: PlayerUi, engine: PlayerEngine): Boolean {
    val st = engine.state.value
    val canPlay = st.playable && st.countdown == null && !ui.panel
    return when {
        e.key == Key.DirectionLeft || e.key == Key.DirectionRight -> {
            if (canPlay) ui.scrub(if (e.key == Key.DirectionRight) 1 else -1, e.repeatCount > 0)
            true
        }
        e.key == Key.DirectionUp -> {
            ui.playPauseFocus.tryFocus()
            true
        }
        e.key == Key.DirectionDown -> true
        e.isSelect -> {
            // A held OK repeats: only its first press acts
            if (e.repeatCount == 0) {
                if (ui.preview != null) ui.commit() else engine.togglePlay()
            }
            true
        }
        else -> false
    }
}

/**
 * While the stream plays, asks the server every 20 s to make the frames around the position in the background
 * (behind it, and ahead inside what is buffered).
 */
@Composable
internal fun TrickPlayPrefetch(engine: PlayerEngine, playing: Boolean, duration: Double) {
    LaunchedEffect(playing, duration > 0) {
        if (!playing || duration <= 0) return@LaunchedEffect
        delay(5000)
        while (true) {
            engine.prefetchThumbs()
            delay(20_000)
        }
    }
}
