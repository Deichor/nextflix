package com.nextflix.tv.feature.account.intro

import android.content.Context
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.LinearGradient
import android.graphics.Paint
import android.graphics.Rect
import android.graphics.Shader
import android.graphics.Typeface
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.core.content.res.ResourcesCompat
import com.nextflix.tv.core.R
import kotlin.math.ceil
import kotlin.math.max

/**
 * The N (depth copies + gradient face) and its hot glow, drawn once into bitmaps (webOS Intro.tsx drawN): the
 * animation then only scales and fades them. Sizes in px; [rem] is 8 dp in px.
 */
internal class NArt(
    val face: ImageBitmap,
    /** face size on screen (px) */
    val faceWidth: Float,
    val faceHeight: Float,
    val hot: ImageBitmap,
    val hotWidth: Float,
    val hotHeight: Float,
    /** the hot glow's top-left relative to the face's top-left */
    val hotOffset: Float,
)

internal fun drawN(context: Context, rem: Float, lite: Boolean): NArt {
    val typeface = runCatching { ResourcesCompat.getFont(context, R.font.inter_extrabold) }.getOrNull()
        ?: Typeface.create(Typeface.SANS_SERIF, Typeface.BOLD)
    val size = 26f * rem
    val probe = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        this.typeface = typeface
        textSize = size
    }
    val bounds = Rect().also { probe.getTextBounds("N", 0, 1, it) }
    val left = -bounds.left.toFloat()
    val ascent = -bounds.top.toFloat()
    val descent = bounds.bottom.toFloat()
    val gw = bounds.width().toFloat()
    val gh = bounds.height().toFloat()
    val depth = (if (lite) 0.45f else 0.9f) * rem

    // Face: more pixels than shown so the zoom stays sharp a little longer
    val ratio = 1.25f
    val fw = gw + depth
    val fh = gh + depth
    val face = Bitmap.createBitmap(ceil(fw * ratio).toInt(), ceil(fh * ratio).toInt(), Bitmap.Config.ARGB_8888)
    Canvas(face).apply {
        scale(ratio, ratio)
        val paint = Paint(probe)
        val depths = if (lite) listOf(Triple(0.25f, 0.45f, 0xFF7D040D.toInt())) else listOf(
            Triple(0.5f, 0.9f, 0xFF3F0207.toInt()),
            Triple(0.25f, 0.45f, 0xFF7D040D.toInt()),
        )
        for ((dx, dy, color) in depths) {
            paint.color = color
            drawText("N", left + dx * rem, ascent + dy * rem, paint)
        }
        paint.color = 0xFFFFFFFF.toInt()
        paint.shader = LinearGradient(
            0f, 0f, 0f, ascent + descent,
            intArrayOf(0xFFFF3A44.toInt(), 0xFFE50914.toInt(), 0xFFA8050E.toInt()),
            floatArrayOf(0f, 0.46f, 1f),
            Shader.TileMode.CLAMP,
        )
        drawText("N", left, ascent, paint)
    }

    // Hot glow: a quarter of the pixels is enough for a blur
    val pad = (if (lite) 4f else 9f) * rem
    val hotScale = 0.5f
    val hw = fw + pad * 2
    val hh = fh + pad * 2
    val hot = Bitmap.createBitmap(max(1, ceil(hw * hotScale).toInt()), max(1, ceil(hh * hotScale).toInt()), Bitmap.Config.ARGB_8888)
    Canvas(hot).apply {
        // Scaled by hand (not with the canvas matrix) so the shadow blur is in known pixels
        val paint = Paint(probe).apply {
            color = 0xFFFF7D84.toInt()
            textSize = size * hotScale
        }
        val shadows = if (lite) listOf(3f to 0xE6FF4652.toInt()) else listOf(8f to 0xB3E50914.toInt(), 3f to 0xE6FF4652.toInt())
        for ((blur, color) in shadows) {
            // Canvas 2D shadowBlur b is a Gaussian with sigma b / 2; Android's radius r has sigma 0.57735 r + 0.5
            val sigma = blur * rem * hotScale / 2f
            paint.setShadowLayer(max(0.5f, (sigma - 0.5f) / 0.57735f), 0f, 0f, color)
            drawText("N", (pad + left) * hotScale, (pad + ascent) * hotScale, paint)
        }
    }
    return NArt(face.asImageBitmap(), fw, fh, hot.asImageBitmap(), hw, hh, -pad)
}
