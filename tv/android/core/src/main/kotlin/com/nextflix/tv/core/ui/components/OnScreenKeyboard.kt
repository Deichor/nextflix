package com.nextflix.tv.core.ui.components

import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.key
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.tv.material3.ClickableSurfaceDefaults
import androidx.tv.material3.Icon
import androidx.tv.material3.Surface
import androidx.tv.material3.Text
import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.i18n.LocalAppLanguage
import com.nextflix.tv.core.i18n.strings
import com.nextflix.tv.core.ui.focus.onKeyDown
import com.nextflix.tv.core.ui.icons.Lucide
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType

enum class KeyboardLayout {
    /** lowercase words; Turkish order with a Turkish profile */
    Search,
    /** server addresses: digits and . : / - _ first */
    Url,
    /** names: Shift (auto at the start and after a space), Turkish letters */
    Text,
}

// Rows of equal length where possible, so Up / Down keep the column (webOS components/Keyboard.tsx)
private const val DIGITS = "1234567890"
private val SEARCH_ROWS = listOf("abcdefghi", "jklmnopqr", "stuvwxyz", DIGITS)
private val SEARCH_ROWS_TR = listOf("abcçdefg", "ğhıijklm", "noöpqrsş", "tuüvwxyz", DIGITS)
private val URL_ROWS = listOf(DIGITS, ".:/-_abcde", "fghijklmno", "pqrstuvwxy", "z")
private val TEXT_ROWS = listOf("abcçdefgğ", "hıijklmno", "öpqrsştuü", "vwxyz'-.", DIGITS)

private fun upper(ch: Char, language: AppLanguage): String = when (ch) {
    'ı' -> "I"
    'i' -> if (language == AppLanguage.Tr) "İ" else "I"
    else -> ch.uppercase()
}

private fun autoShift(v: String) = v.isEmpty() || v.endsWith(' ')

private val LocalKeyHeight = staticCompositionLocalOf { 32.dp }

/**
 * On-screen keyboard for the remote. Typing on a hardware keyboard (or the remote's number keys) works while a
 * key has focus. [firstKey] focuses the first letter key (e.g. FocusRestore fallback). [keyHeight]: 27 dp for the
 * compact search keyboard.
 */
@Composable
fun OnScreenKeyboard(
    layout: KeyboardLayout,
    value: String,
    onValueChange: (String) -> Unit,
    modifier: Modifier = Modifier,
    width: Dp = 380.dp,
    max: Int = 120,
    onVoice: (() -> Unit)? = null,
    firstKey: FocusRequester? = null,
    keyHeight: Dp = 32.dp,
) {
    val language = LocalAppLanguage.current
    val s = strings()
    val text = layout == KeyboardLayout.Text
    var shift by rememberSaveable(layout) { mutableStateOf(text && autoShift(value)) }
    val current by rememberUpdatedState(value)
    val change by rememberUpdatedState(onValueChange)

    fun set(v: String) {
        change(v)
        if (text) shift = autoShift(v)
    }

    fun type(ch: Char) {
        if (current.length >= max) return
        set(current + if (text && shift) upper(ch, language) else ch.toString())
    }

    val rows = when (layout) {
        KeyboardLayout.Url -> URL_ROWS
        KeyboardLayout.Text -> TEXT_ROWS
        KeyboardLayout.Search -> if (language == AppLanguage.Tr) SEARCH_ROWS_TR else SEARCH_ROWS
    }
    val cols = rows.filter { it != DIGITS }.maxOf { it.length }

    CompositionLocalProvider(LocalKeyHeight provides keyHeight) {
        Column(
            modifier
                .width(width)
                .onKeyDown { e ->
                    when {
                        e.key == Key.Backspace || e.key == Key.Delete -> {
                            if (current.isEmpty()) false else {
                                set(current.dropLast(1))
                                true
                            }
                        }
                        else -> {
                            val ch = e.nativeKeyEvent.unicodeChar.takeIf { it > 31 }?.toChar() ?: return@onKeyDown false
                            if (ch == ' ' && layout == KeyboardLayout.Url) return@onKeyDown false
                            if (current.length < max) set(current + if (layout == KeyboardLayout.Search) ch.lowercaseChar() else ch)
                            true
                        }
                    }
                },
            verticalArrangement = Arrangement.spacedBy(4.dp),
        ) {
            rows.forEachIndexed { r, row ->
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                    row.forEachIndexed { i, ch ->
                        val label = if (text && shift) upper(ch, language) else ch.toString()
                        KeyButton(
                            onClick = { type(ch) },
                            modifier = Modifier
                                .weight(1f)
                                .then(if (r == 0 && i == 0 && firstKey != null) Modifier.focusRequester(firstKey) else Modifier),
                            label = label,
                        )
                    }
                    if (row != DIGITS && row.length < cols && layout != KeyboardLayout.Url) {
                        repeat(cols - row.length) { Spacer(Modifier.weight(1f)) }
                    }
                    if (layout == KeyboardLayout.Url && row.length < cols) {
                        repeat(cols - row.length) { Spacer(Modifier.weight(1f)) }
                    }
                }
            }
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                if (onVoice != null) KeyButton(onVoice, Modifier.weight(1.5f), icon = Lucide.Mic, description = s.keyboardVoice)
                if (text) KeyButton({ shift = !shift }, Modifier.weight(1.5f), icon = Lucide.ArrowBigUp, description = s.keyboardShift, active = shift)
                if (layout != KeyboardLayout.Url) KeyButton({ type(' ') }, Modifier.weight(3f), label = s.keyboardSpace)
                KeyButton({ set(current.dropLast(1)) }, Modifier.weight(1.5f), icon = Lucide.Delete, description = s.keyboardDelete)
                KeyButton({ set("") }, Modifier.weight(2f), label = s.keyboardClear)
            }
        }
    }
}

@Composable
private fun KeyButton(
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    label: String? = null,
    icon: ImageVector? = null,
    description: String? = null,
    active: Boolean = false,
) {
    Surface(
        onClick = onClick,
        modifier = modifier.height(LocalKeyHeight.current),
        shape = ClickableSurfaceDefaults.shape(RoundedCornerShape(3.dp)),
        colors = ClickableSurfaceDefaults.colors(
            containerColor = if (active) Color.White.copy(alpha = 0.3f) else Color.White.copy(alpha = 0.08f),
            contentColor = Color.White,
            focusedContainerColor = Color.White,
            focusedContentColor = Color.Black,
        ),
        scale = ClickableSurfaceDefaults.scale(focusedScale = 1.1f),
    ) {
        Box(Modifier.align(Alignment.Center)) {
            when {
                icon != null -> Icon(icon, contentDescription = description, modifier = Modifier.size(16.dp))
                label != null -> Text(label, style = NfType.Button.copy(fontSize = 14.sp), maxLines = 1)
            }
        }
    }
}

/** The text field above a keyboard: shows [value] (or [placeholder]) with a blinking caret. */
@Composable
fun KeyboardField(value: String, modifier: Modifier = Modifier, placeholder: String = "") {
    val caret by rememberInfiniteTransition(label = "caret").animateFloat(
        initialValue = 1f,
        targetValue = 0f,
        animationSpec = infiniteRepeatable(tween(500), RepeatMode.Reverse),
        label = "blink",
    )
    Row(
        modifier
            .height(44.dp)
            .background(Color(0x80000000))
            .border(BorderStroke(1.6.dp, Color.White.copy(alpha = 0.4f)))
            .padding(horizontal = 13.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (value.isEmpty() && placeholder.isNotEmpty()) {
            Box(Modifier.width(1.2.dp).height(20.dp).alpha(caret).background(Color.White))
            Text(placeholder, style = NfType.Body.copy(fontSize = 17.sp), color = NfColors.Dim, maxLines = 1, modifier = Modifier.padding(start = 4.dp))
        } else {
            Text(value, style = NfType.Body.copy(fontSize = 17.sp), maxLines = 1, overflow = TextOverflow.StartEllipsis)
            Box(Modifier.padding(start = 1.dp).width(1.2.dp).height(20.dp).alpha(caret).background(Color.White))
        }
    }
}
