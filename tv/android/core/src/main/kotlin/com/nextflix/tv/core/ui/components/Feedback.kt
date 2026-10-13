package com.nextflix.tv.core.ui.components

import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.tv.material3.Text
import com.nextflix.tv.core.i18n.strings
import com.nextflix.tv.core.ui.focus.tryFocus
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType

/** Red loading ring. */
@Composable
fun Spinner(modifier: Modifier = Modifier, size: Dp = 40.dp) {
    val angle by rememberInfiniteTransition(label = "spinner").animateFloat(
        initialValue = 0f,
        targetValue = 360f,
        animationSpec = infiniteRepeatable(tween(900, easing = LinearEasing)),
        label = "angle",
    )
    Canvas(modifier.size(size)) {
        val stroke = this.size.minDimension * 0.09f
        drawArc(NfColors.Red.copy(alpha = 0.25f), 0f, 360f, false, style = Stroke(stroke))
        drawArc(NfColors.Red, angle, 90f, false, style = Stroke(stroke, cap = StrokeCap.Round))
    }
}

/** Centered spinner filling its parent. */
@Composable
fun LoadingBox(modifier: Modifier = Modifier) {
    Box(modifier.fillMaxSize(), contentAlignment = Alignment.Center) { Spinner() }
}

/** A failed load: the message and a focused Retry button. */
@Composable
fun ErrorMessage(message: String, onRetry: (() -> Unit)?, modifier: Modifier = Modifier) {
    val retry = remember { FocusRequester() }
    Column(modifier.fillMaxSize(), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.Center) {
        Text(message, style = NfType.Body, color = NfColors.Muted, textAlign = TextAlign.Center, modifier = Modifier.widthIn(max = 480.dp))
        if (onRetry != null) {
            Spacer(Modifier.height(16.dp))
            NfButton(strings().retry, onRetry, Modifier.focusRequester(retry))
            LaunchedEffect(Unit) { retry.tryFocus() }
        }
    }
}

/**
 * Modal question with two buttons (exit app, delete profile). Cancel has the focus first; Back cancels.
 * Shown in its own window, so focus cannot leave it.
 */
@Composable
fun ConfirmDialog(
    title: String,
    confirm: String,
    cancel: String,
    onConfirm: () -> Unit,
    onCancel: () -> Unit,
    message: String? = null,
) {
    Dialog(onDismissRequest = onCancel, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        val cancelFocus = remember { FocusRequester() }
        Box(Modifier.fillMaxSize().background(NfColors.Scrim), contentAlignment = Alignment.Center) {
            Column(
                Modifier
                    .widthIn(max = 520.dp)
                    .background(NfColors.Background, RoundedCornerShape(8.dp))
                    .padding(32.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
            ) {
                Text(title, style = NfType.PageTitle.copy(fontSize = NfType.PageTitle.fontSize * 0.8f), textAlign = TextAlign.Center)
                if (message != null) {
                    Spacer(Modifier.height(12.dp))
                    Text(message, style = NfType.Body, color = NfColors.Muted, textAlign = TextAlign.Center)
                }
                Spacer(Modifier.height(24.dp))
                Row(horizontalArrangement = Arrangement.Center) {
                    NfButton(confirm, onConfirm)
                    Spacer(Modifier.width(12.dp))
                    NfButton(cancel, onCancel, Modifier.focusRequester(cancelFocus), style = ButtonStyle.Secondary)
                }
            }
        }
        LaunchedEffect(Unit) { cancelFocus.tryFocus() }
    }
}

/** The NEXTFLIX wordmark. */
@Composable
fun Wordmark(modifier: Modifier = Modifier) {
    Text("NEXTFLIX", style = NfType.Brand, modifier = modifier)
}
