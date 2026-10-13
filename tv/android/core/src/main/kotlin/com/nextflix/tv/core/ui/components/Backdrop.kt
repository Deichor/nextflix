package com.nextflix.tv.core.ui.components

import androidx.compose.animation.Crossfade
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import coil3.SingletonImageLoader
import coil3.compose.AsyncImage
import coil3.request.ImageRequest
import coil3.request.SuccessResult
import com.nextflix.tv.core.ui.theme.NfColors

/**
 * Large backdrop image that cross-fades when [url] changes (heroes, detail page), with the left and bottom
 * shades that keep text on top of it readable. [url] "" shows only the background.
 */
@Composable
fun Backdrop(url: String, modifier: Modifier = Modifier, shade: Boolean = true) {
    Box(modifier.fillMaxSize()) {
        FadeInImage(url, Modifier.fillMaxSize())
        if (shade) {
            Box(
                Modifier
                    .fillMaxSize()
                    .background(Brush.horizontalGradient(0f to NfColors.Background, 0.45f to NfColors.Background.copy(alpha = 0.6f), 0.75f to Color.Transparent)),
            )
            Box(
                Modifier
                    .fillMaxSize()
                    .background(Brush.verticalGradient(0.55f to Color.Transparent, 1f to NfColors.Background)),
            )
        }
    }
}

/**
 * Downloads and decodes [url] first, then fades it in over the previous image, so a focus move never shows an
 * empty backdrop while the next one loads. A failed image keeps the previous one.
 */
@Composable
fun FadeInImage(url: String, modifier: Modifier = Modifier) {
    val context = LocalContext.current
    var shown by remember { mutableStateOf("") }
    LaunchedEffect(url) {
        if (url.isEmpty()) {
            shown = ""
            return@LaunchedEffect
        }
        val request = ImageRequest.Builder(context).data(url).build()
        if (SingletonImageLoader.get(context).execute(request) is SuccessResult) shown = url
    }
    Crossfade(targetState = shown, animationSpec = tween(450), modifier = modifier, label = "backdrop") { target ->
        if (target.isNotEmpty()) {
            AsyncImage(model = target, contentDescription = null, contentScale = ContentScale.Crop, modifier = Modifier.fillMaxSize())
        }
    }
}
