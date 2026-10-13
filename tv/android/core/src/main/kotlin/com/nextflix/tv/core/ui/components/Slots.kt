package com.nextflix.tv.core.ui.components

import androidx.compose.runtime.Composable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Modifier
import androidx.tv.material3.Text
import com.nextflix.tv.core.prefs.SubtitleStyle
import com.nextflix.tv.core.ui.theme.NfType

/**
 * Draws a subtitle preview in a style. Given a bounded box it draws a scaled small screen (dimming band,
 * position, size); otherwise just the line.
 */
fun interface SubtitleSampleRenderer {
    @Composable
    fun Render(text: String, style: SubtitleStyle, modifier: Modifier)
}

/**
 * Subtitle preview for screens outside the player (Settings). The app provides :feature:subtitles' SubtitleSample
 * here, so :feature:account can show it without depending on :feature:subtitles.
 */
val LocalSubtitleSample = staticCompositionLocalOf<SubtitleSampleRenderer> {
    SubtitleSampleRenderer { text, _, modifier -> Text(text, modifier = modifier, style = NfType.Body) }
}
