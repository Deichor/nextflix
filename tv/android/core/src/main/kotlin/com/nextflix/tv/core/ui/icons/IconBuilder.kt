package com.nextflix.tv.core.ui.icons

import androidx.compose.foundation.Canvas
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.CompositingStrategy
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.graphics.drawscope.Fill
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.scale
import androidx.compose.ui.graphics.drawscope.translate
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.graphics.vector.PathParser
import androidx.compose.ui.graphics.vector.VectorPath
import androidx.compose.ui.graphics.vector.addPathNodes
import androidx.compose.ui.unit.dp

/** A 24x24 line icon from SVG path data (stroke 2, round caps and joins, no fill). */
internal fun lucide(name: String, vararg paths: String, filled: Boolean = false): ImageVector =
    ImageVector.Builder(name = name, defaultWidth = 24.dp, defaultHeight = 24.dp, viewportWidth = 24f, viewportHeight = 24f)
        .apply {
            paths.forEach { d ->
                addPath(
                    pathData = addPathNodes(d),
                    fill = if (filled) SolidColor(Color.White) else null,
                    stroke = SolidColor(Color.White),
                    strokeLineWidth = 2f,
                    strokeLineCap = StrokeCap.Round,
                    strokeLineJoin = StrokeJoin.Round,
                )
            }
        }
        .build()

/** The same line icon with its shapes filled (Play, a chosen thumbs rating). */
fun ImageVector.filled(): ImageVector {
    val paths = root.mapNotNull { it as? VectorPath }.map { it.pathData }
    return ImageVector.Builder(name = "$name-filled", defaultWidth = defaultWidth, defaultHeight = defaultHeight, viewportWidth = viewportWidth, viewportHeight = viewportHeight)
        .apply {
            paths.forEach { nodes ->
                addPath(
                    pathData = nodes,
                    fill = SolidColor(Color.White),
                    stroke = SolidColor(Color.White),
                    strokeLineWidth = 2f,
                    strokeLineCap = StrokeCap.Round,
                    strokeLineJoin = StrokeJoin.Round,
                )
            }
        }
        .build()
}

private const val THUMB =
    "M7 10v12M15 5.88 14 10h5.83a2 2 0 0 1 1.92 2.56l-2.33 8A2 2 0 0 1 17.5 22H4a2 2 0 0 1-2-2v-8a2 2 0 0 1 2-2h2.76a2 2 0 0 0 1.79-1.11L12 2a3.13 3.13 0 0 1 3 3.88Z"
private const val OUTLINE =
    "M15 5.88 14 10h5.83a2 2 0 0 1 1.92 2.56l-2.33 8A2 2 0 0 1 17.5 22H4a2 2 0 0 1-2-2v-8a2 2 0 0 1 2-2h2.76a2 2 0 0 0 1.79-1.11L12 2a3.13 3.13 0 0 1 3 3.88Z"

private val thumbPath: Path by lazy { PathParser().parsePathString(THUMB).toPath() }
private val outlinePath: Path by lazy { PathParser().parsePathString(OUTLINE).toPath() }

/**
 * "Love this" icon (webOS components/DoubleThumbsUp.tsx): two thumbs-up, the back one cut where the front one
 * sits so no lines cross. [filled] for a chosen rating.
 */
@Composable
fun DoubleThumbsUp(color: Color, modifier: Modifier = Modifier, filled: Boolean = false) {
    Canvas(modifier.graphicsLayer { compositingStrategy = CompositingStrategy.Offscreen }) {
        val unit = size.minDimension / 24f
        scale(unit, unit, pivot = Offset.Zero) {
            thumb(color, filled, dx = 0f, dy = 0f)
            translate(4.8f, 4.8f) {
                scale(0.8f, 0.8f, pivot = Offset.Zero) {
                    drawPath(outlinePath, Color.Black, style = Fill, blendMode = BlendMode.Clear)
                    drawPath(outlinePath, Color.Black, style = Stroke(width = 5.5f, join = StrokeJoin.Round), blendMode = BlendMode.Clear)
                }
            }
            thumb(color, filled, dx = 4.8f, dy = 4.8f)
        }
    }
}

private fun DrawScope.thumb(color: Color, filled: Boolean, dx: Float, dy: Float) {
    translate(dx, dy) {
        scale(0.8f, 0.8f, pivot = Offset.Zero) {
            if (filled) drawPath(thumbPath, color, style = Fill)
            drawPath(thumbPath, color, style = Stroke(width = 2f, cap = StrokeCap.Round, join = StrokeJoin.Round))
        }
    }
}
