package com.nextflix.tv.core.ui.focus

import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEvent
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onKeyEvent
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type
import com.nextflix.tv.core.nav.LocalNavEntry
import kotlinx.coroutines.launch

/**
 * Marks a focusable element (apply before the focusable itself, e.g. on a Surface's modifier) as restorable under
 * [key]: when it gets focus it becomes the screen's remembered element, and [FocusRestore] focuses it again when
 * the screen comes back (Back from Detail / Player). Keys must be unique on the screen ("row-trending:movie-27205").
 */
@Composable
fun Modifier.restorableFocus(key: String): Modifier {
    val entry = LocalNavEntry.current ?: return this
    val requester = remember(key) { FocusRequester() }
    DisposableEffect(entry, key, requester) {
        entry.requesters[key] = requester
        onDispose { if (entry.requesters[key] === requester) entry.requesters.remove(key) }
    }
    return this
        .focusRequester(requester)
        .onFocusChanged { if (it.isFocused) entry.focusedKey = key }
}

/**
 * Initial focus of a screen, once [ready] (its first data are in): the element focused when the screen was left,
 * else the restorable element [fallbackKey], else [fallback]. Runs once per visit of the screen; waits a few frames
 * for lazy rows to compose the element.
 */
@Composable
fun FocusRestore(ready: Boolean, fallbackKey: String? = null, fallback: FocusRequester? = null) {
    val entry = LocalNavEntry.current
    var done by remember { mutableStateOf(false) }
    LaunchedEffect(ready, done) {
        if (!ready || done) return@LaunchedEffect
        repeat(10) {
            withFrameNanos { }
            val target = entry?.let { e -> e.focusedKey?.let { e.requesters[it] } ?: fallbackKey?.let { e.requesters[it] } }
            if (target != null && target.tryFocus()) {
                done = true
                return@LaunchedEffect
            }
        }
        fallback?.tryFocus()
        done = true
    }
}

/**
 * Apply on a screen's root: when the focused restorable element leaves the composition while it has the focus
 * (a reload removed the card), focuses the first restorable element of [candidates] (given the lost key) that is
 * on screen. Nothing happens when the focus merely moved elsewhere.
 */
@Composable
fun Modifier.rescueFocus(candidates: (lostKey: String) -> List<String?>): Modifier {
    val entry = LocalNavEntry.current ?: return this
    val scope = rememberCoroutineScope()
    val latest by rememberUpdatedState(candidates)
    val focus = remember { FocusFlag() }
    return onFocusChanged { f ->
        val lost = focus.has && !f.hasFocus
        focus.has = f.hasFocus
        val key = entry.focusedKey
        if (!lost || key == null) return@onFocusChanged
        scope.launch {
            // The removed element unregisters its requester once the frame is applied
            repeat(2) { withFrameNanos { } }
            if (focus.has || entry.requesters[key] != null) return@launch
            latest(key).firstNotNullOfOrNull { k -> k?.let { entry.requesters[it] } }?.tryFocus()
        }
    }
}

private class FocusFlag(var has: Boolean = false)

/** requestFocus that never throws (the node may not be attached yet). */
fun FocusRequester.tryFocus(): Boolean = try {
    requestFocus()
} catch (_: IllegalStateException) {
    false
}

/** Remote keys. DPAD_CENTER, ENTER and NUMPAD_ENTER all count as OK. */
object Keys {
    val Select = setOf(Key.DirectionCenter, Key.Enter, Key.NumPadEnter)
    val Back = setOf(Key.Back, Key.Escape)
    val Play = setOf(Key.MediaPlay)
    val Pause = setOf(Key.MediaPause)
    val PlayPause = setOf(Key.MediaPlayPause)
    val Stop = setOf(Key.MediaStop)
    val Rewind = setOf(Key.MediaRewind)
    val FastForward = setOf(Key.MediaFastForward)
    val Next = setOf(Key.MediaNext)
    val Previous = setOf(Key.MediaPrevious)
}

val KeyEvent.isDown: Boolean get() = type == KeyEventType.KeyDown
val KeyEvent.isSelect: Boolean get() = key in Keys.Select
val KeyEvent.isBack: Boolean get() = key in Keys.Back

/** How many times a held key repeated (0 for the first press). */
val KeyEvent.repeatCount: Int get() = nativeKeyEvent.repeatCount

/** Handles key-down events that reach this element (after its focused children). Return true to consume. */
fun Modifier.onKeyDown(handler: (KeyEvent) -> Boolean): Modifier =
    onKeyEvent { e -> e.type == KeyEventType.KeyDown && handler(e) }

/** Like [onKeyDown], but before the focused children see the key (screen-wide keys such as media keys). */
fun Modifier.onPreviewKeyDown(handler: (KeyEvent) -> Boolean): Modifier =
    onPreviewKeyEvent { e -> e.type == KeyEventType.KeyDown && handler(e) }
