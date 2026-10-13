package com.nextflix.tv.feature.player

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.focusGroup
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.tv.material3.ClickableSurfaceDefaults
import androidx.tv.material3.Icon
import androidx.tv.material3.LocalContentColor
import androidx.tv.material3.Surface
import androidx.tv.material3.Text
import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.i18n.LocalAppLanguage
import com.nextflix.tv.core.prefs.SubtitleStyle
import com.nextflix.tv.core.prefs.subtitleStyleStrings
import com.nextflix.tv.core.ui.components.Spinner
import com.nextflix.tv.core.ui.focus.onKeyDown
import com.nextflix.tv.core.ui.focus.tryFocus
import com.nextflix.tv.core.ui.icons.Lucide
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType
import com.nextflix.tv.feature.subtitles.Languages
import com.nextflix.tv.feature.subtitles.SubtitleState
import kotlinx.coroutines.delay

/** The panel closes by itself after this long without a key. */
private const val PANEL_IDLE_MS = 15_000L
private const val SUB_DELAY_STEP_S = 0.5

private val PanelShape = RoundedCornerShape(6.dp)
private val RowShape = RoundedCornerShape(3.dp)

private enum class PanelView { Tracks, Style }

/**
 * Audio & Subtitles (webOS player/AudioSubtitlePanel.tsx): audio tracks on the left, one row per subtitle language
 * on the right, then "Subtitle timing" (Left / Right shift by half a second, OK resets) while a subtitle is on, and
 * "Subtitle appearance", which swaps in the style view (Left / Right or OK change a value, the player shows a live
 * sample). Closes on Back, Left past the audio column and after PANEL_IDLE_MS without a key.
 */
@Composable
internal fun BoxScope.AudioSubtitlePanel(
    s: PlayerState,
    subs: SubtitleState,
    style: SubtitleStyle,
    onAudio: (Int) -> Unit,
    onSubtitle: (String?) -> Unit,
    /** sets the timing; returns the value it took (rounded, limited) */
    onSubDelay: (Double) -> Double,
    onSubAlt: () -> Unit,
    onStyle: (SubtitleStyle) -> Unit,
    onClose: () -> Unit,
    onStyling: (Boolean) -> Unit,
) {
    var view by remember { mutableStateOf(PanelView.Tracks) }
    var idle by remember { mutableIntStateOf(0) }
    val firstFocus = remember { FocusRequester() }
    val styleRowFocus = remember { FocusRequester() }
    val styleFirstFocus = remember { FocusRequester() }
    var firstView by remember { mutableStateOf(true) }

    LaunchedEffect(view) {
        onStyling(view == PanelView.Style)
        withFrameNanos { }
        when {
            firstView -> firstFocus.tryFocus()
            view == PanelView.Style -> styleFirstFocus.tryFocus()
            else -> styleRowFocus.tryFocus()
        }
        firstView = false
    }
    LaunchedEffect(idle) {
        delay(PANEL_IDLE_MS)
        onClose()
    }
    BackHandler(enabled = view == PanelView.Style) { view = PanelView.Tracks }

    val screenHeight = LocalConfiguration.current.screenHeightDp.dp
    val style0 = view == PanelView.Style
    Row(
        Modifier
            .align(if (style0) Alignment.TopEnd else Alignment.BottomEnd)
            .padding(end = 32.dp, top = if (style0) 64.dp else 0.dp, bottom = if (style0) 0.dp else 112.dp)
            .width(if (style0) 400.dp else 656.dp)
            .heightIn(max = screenHeight * 0.7f)
            .background(Color(0xF2141414), PanelShape)
            .border(1.dp, Color.White.copy(alpha = 0.1f), PanelShape)
            .padding(vertical = 16.dp)
            .onPreviewKeyEvent {
                idle++
                false
            }
            .focusGroup(),
    ) {
        if (view == PanelView.Tracks) {
            Tracks(s, subs, firstFocus, styleRowFocus, onAudio, onSubtitle, onSubDelay, onSubAlt, onClose) { view = PanelView.Style }
        } else {
            StyleView(style, styleFirstFocus, onStyle) { view = PanelView.Tracks }
        }
    }
}

@Composable
private fun RowScope.Tracks(
    s: PlayerState,
    subs: SubtitleState,
    firstFocus: FocusRequester,
    styleRowFocus: FocusRequester,
    onAudio: (Int) -> Unit,
    onSubtitle: (String?) -> Unit,
    onSubDelay: (Double) -> Double,
    onSubAlt: () -> Unit,
    onClose: () -> Unit,
    onStyleView: () -> Unit,
) {
    val t = playerStrings.current()
    val language = LocalAppLanguage.current
    val original = langOf(s.meta.originalLanguage)
    val tracks = s.audioTracks.ifEmpty { listOf(AudioTrack(0, "", "", "", 0)) }
    val active = subs.tracks.firstOrNull { it.id == subs.selectedId }
    val activeLang = active?.let { langOf(it.language).ifEmpty { it.language } }
    // The first focus lands on the active audio track
    val activeAudio = tracks.firstOrNull { it.index == s.audio } ?: tracks.first()

    PanelColumn(t.audio, Modifier.weight(1f)) {
        tracks.forEachIndexed { i, track ->
            val code = langOf(track.language)
            val name = if (code.isNotEmpty()) Languages.name(language, code) else t.audioN(i + 1)
            val label = listOf(name, channelLabel(t, track.channels)).filter { it.isNotEmpty() }.joinToString(" · ")
            PanelRow(
                label = label,
                note = if (code.isNotEmpty() && code == original) " [${t.original}]" else null,
                active = track.index == s.audio,
                modifier = Modifier
                    .then(if (track === activeAudio) Modifier.focusRequester(firstFocus) else Modifier)
                    .onKeyDown { e ->
                        // Left past the audio column closes the panel
                        if (e.key == Key.DirectionLeft) {
                            onClose()
                            true
                        } else {
                            false
                        }
                    },
                onClick = { onAudio(track.index) },
            )
        }
    }
    val divider = Color.White.copy(alpha = 0.1f)
    PanelColumn(t.subtitles, Modifier.weight(1f).drawBehind { drawLine(divider, Offset.Zero, Offset(0f, size.height), 1.dp.toPx()) }) {
        PanelRow(label = t.subtitlesOff, active = subs.selectedId == null, onClick = { onSubtitle(null) })
        if (subs.tracks.isEmpty()) {
            if (subs.searching) {
                Row(Modifier.padding(horizontal = 11.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                    Spinner(size = 14.dp)
                    Spacer(Modifier.width(8.dp))
                    Hint(t.subtitlesSearching)
                }
            } else {
                Hint(t.subtitlesNone, Modifier.padding(horizontal = 11.dp, vertical = 8.dp))
            }
        }
        subs.menu.forEach { item ->
            val code = langOf(item.language).ifEmpty { item.language }
            val name = if (langOf(item.language).isNotEmpty()) Languages.name(language, code) else item.label.ifEmpty { item.language }
            PanelRow(
                label = name + if (item.source == "ai") " · ${t.subAi}" else "",
                active = activeLang == code,
                onClick = { onSubtitle(item.id) },
            )
        }
        if (s.subAlt) PanelRow(label = t.subAltRow, active = false, onClick = onSubAlt)
        if (subs.selectedId != null) DelayRow(subs.delaySeconds, language, onSubDelay)
        PanelRow(
            label = subtitleStyleStrings.current().title,
            active = false,
            icon = Lucide.Type,
            trailing = Lucide.ChevronRight,
            modifier = Modifier.padding(top = 5.dp).focusRequester(styleRowFocus),
            onClick = onStyleView,
        )
    }
}

@Composable
private fun DelayRow(value: Double, language: AppLanguage, onSubDelay: (Double) -> Double) {
    val t = playerStrings.current()
    // Held keys repeat faster than the state comes back: count on from the value just set
    var current by remember { mutableStateOf(value) }
    LaunchedEffect(value) { current = value }
    var focused by remember { mutableStateOf(false) }
    ValueRow(
        name = t.subtitleDelay,
        value = t.seconds(delayText(current, language == AppLanguage.Tr)),
        modifier = Modifier
            .padding(top = 5.dp)
            .onFocusChanged { focused = it.isFocused }
            .onKeyDown { e ->
                when (e.key) {
                    Key.DirectionLeft, Key.DirectionRight -> {
                        current = onSubDelay(current + if (e.key == Key.DirectionRight) SUB_DELAY_STEP_S else -SUB_DELAY_STEP_S)
                        true
                    }
                    else -> false
                }
            },
        onClick = { current = onSubDelay(0.0) },
    )
    if (focused) Hint(t.subtitleDelayHint, Modifier.padding(horizontal = 11.dp, vertical = 8.dp))
}

@Composable
private fun StyleView(style: SubtitleStyle, firstFocus: FocusRequester, onStyle: (SubtitleStyle) -> Unit, onDone: () -> Unit) {
    val language = LocalAppLanguage.current
    val strings = subtitleStyleStrings.current()
    // Held keys: count on from the value just set
    var current by remember { mutableStateOf(style) }
    LaunchedEffect(style) { current = style }
    fun change(option: SubtitleStyle.Option, step: Int) {
        current = current.cycle(option, step)
        onStyle(current)
    }
    val consumeSides = Modifier.onKeyDown { e -> e.key == Key.DirectionLeft || e.key == Key.DirectionRight }
    PanelColumn(strings.title, Modifier.fillMaxWidth()) {
        SubtitleStyle.Option.entries.forEachIndexed { i, option ->
            ValueRow(
                name = SubtitleStyle.optionLabel(option, language),
                value = current.valueLabel(option, language),
                modifier = Modifier
                    .then(if (i == 0) Modifier.focusRequester(firstFocus) else Modifier)
                    .onKeyDown { e ->
                        when (e.key) {
                            Key.DirectionLeft, Key.DirectionRight -> {
                                change(option, if (e.key == Key.DirectionRight) 1 else -1)
                                true
                            }
                            else -> false
                        }
                    },
                onClick = { change(option, 1) },
            )
        }
        Spacer(Modifier.height(8.dp))
        PanelRow(label = strings.reset, active = true, plain = true, modifier = consumeSides, onClick = {
            current = SubtitleStyle()
            onStyle(current)
        })
        PanelRow(label = strings.done, active = true, plain = true, modifier = consumeSides, onClick = onDone)
    }
}

@Composable
private fun PanelColumn(title: String, modifier: Modifier, content: @Composable ColumnScope.() -> Unit) {
    Column(modifier.padding(horizontal = 13.dp)) {
        Text(
            title,
            modifier = Modifier.padding(start = 11.dp, end = 11.dp, bottom = 10.dp),
            style = NfType.Body.copy(fontSize = 19.sp, fontWeight = FontWeight.ExtraBold),
        )
        Column(Modifier.verticalScroll(rememberScrollState())) { content() }
    }
}

@Composable
private fun Hint(text: String, modifier: Modifier = Modifier) {
    Text(text, modifier = modifier, style = NfType.Body.copy(fontSize = 12.sp), color = NfColors.Dim)
}

private val rowColors
    @Composable get() = ClickableSurfaceDefaults.colors(
        containerColor = Color.Transparent,
        contentColor = NfColors.Muted,
        focusedContainerColor = Color.White,
        focusedContentColor = Color.Black,
    )

/** One choice: a red check when active (webOS .panel-row); [plain]: no check column. */
@Composable
private fun PanelRow(
    label: String,
    active: Boolean,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    note: String? = null,
    icon: ImageVector? = null,
    trailing: ImageVector? = null,
    plain: Boolean = false,
) {
    var focused by remember { mutableStateOf(false) }
    Surface(
        onClick = onClick,
        modifier = modifier.fillMaxWidth().onFocusChanged { focused = it.isFocused },
        shape = ClickableSurfaceDefaults.shape(RowShape),
        colors = rowColors,
        scale = ClickableSurfaceDefaults.scale(focusedScale = 1f),
    ) {
        Row(Modifier.padding(horizontal = 11.dp, vertical = 9.dp), verticalAlignment = Alignment.CenterVertically) {
            if (!plain) {
                Box(Modifier.width(24.dp)) {
                    when {
                        icon != null -> Icon(icon, null, Modifier.size(18.dp), tint = if (focused) Color.Black else NfColors.Muted)
                        active -> Icon(Lucide.Check, null, Modifier.size(18.dp), tint = NfColors.Red)
                    }
                }
            }
            val color = if (focused) Color.Black else if (active || icon != null) Color.White else LocalContentColor.current
            Text(
                label,
                modifier = Modifier.weight(1f, fill = false),
                style = NfType.Body.copy(fontSize = 14.sp, fontWeight = if (active && !plain) FontWeight.SemiBold else FontWeight.Normal),
                color = color,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            if (note != null) {
                Text(note, style = NfType.Body.copy(fontSize = 11.sp), color = if (focused) Color.Black else NfColors.Dim, maxLines = 1)
            }
            if (trailing != null) {
                Spacer(Modifier.weight(1f))
                Icon(trailing, null, Modifier.size(18.dp))
            }
        }
    }
}

/** A setting changed with Left / Right (webOS .style-row): name, ‹ value ›. */
@Composable
private fun ValueRow(name: String, value: String, modifier: Modifier, onClick: () -> Unit) {
    var focused by remember { mutableStateOf(false) }
    Surface(
        onClick = onClick,
        modifier = modifier.fillMaxWidth().onFocusChanged { focused = it.isFocused },
        shape = ClickableSurfaceDefaults.shape(RowShape),
        colors = rowColors,
        scale = ClickableSurfaceDefaults.scale(focusedScale = 1f),
    ) {
        Row(Modifier.padding(horizontal = 11.dp, vertical = 9.dp), verticalAlignment = Alignment.CenterVertically) {
            Text(name, modifier = Modifier.weight(1f), style = NfType.Body.copy(fontSize = 14.sp), maxLines = 1)
            val arrows = if (focused) Color.Black else Color.White.copy(alpha = 0.45f)
            Icon(Lucide.ChevronLeft, null, Modifier.size(18.dp), tint = arrows)
            Text(
                value,
                modifier = Modifier.width(136.dp),
                style = NfType.Body.copy(fontSize = 14.sp, fontWeight = FontWeight.SemiBold),
                color = if (focused) Color.Black else Color.White,
                textAlign = TextAlign.Center,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            Icon(Lucide.ChevronRight, null, Modifier.size(18.dp), tint = arrows)
        }
    }
}

private fun channelLabel(t: PlayerStrings, channels: Int): String = when {
    channels == 1 -> t.mono
    channels == 2 -> t.stereo
    channels == 6 -> "5.1"
    channels == 8 -> "7.1"
    channels > 2 -> "${channels}ch"
    else -> ""
}
