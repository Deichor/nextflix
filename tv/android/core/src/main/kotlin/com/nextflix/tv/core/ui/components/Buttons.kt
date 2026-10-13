package com.nextflix.tv.core.ui.components

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Shape
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.tv.material3.Border
import androidx.tv.material3.ClickableSurfaceDefaults
import androidx.tv.material3.Icon
import androidx.tv.material3.Surface
import androidx.tv.material3.Text
import com.nextflix.tv.core.ui.theme.Dimens
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType

enum class ButtonStyle {
    /** white, black text (Play, Save) */
    Primary,
    /** grey see-through, white when focused (More Info, Cancel) */
    Secondary,
    /** no background, muted text, white when focused */
    Ghost,
}

private val ButtonShape = RoundedCornerShape(4.dp)

/** The 10-foot button of the webOS app: 35 dp tall, scales to 1.08 with a white ring when focused. */
@Composable
fun NfButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    icon: ImageVector? = null,
    style: ButtonStyle = ButtonStyle.Primary,
    enabled: Boolean = true,
    shape: Shape = ButtonShape,
    padding: Dp = 20.dp,
) {
    val (container, content) = when (style) {
        ButtonStyle.Primary -> Color.White to Color.Black
        ButtonStyle.Secondary -> NfColors.Secondary to Color.White
        ButtonStyle.Ghost -> Color.Transparent to NfColors.Muted
    }
    Surface(
        onClick = onClick,
        enabled = enabled,
        modifier = modifier.height(Dimens.ButtonHeight),
        shape = ClickableSurfaceDefaults.shape(shape),
        colors = ClickableSurfaceDefaults.colors(
            containerColor = container,
            contentColor = content,
            focusedContainerColor = Color.White,
            focusedContentColor = Color.Black,
            pressedContainerColor = Color.White,
            pressedContentColor = Color.Black,
            disabledContainerColor = Color.White.copy(alpha = 0.22f),
            disabledContentColor = Color.White.copy(alpha = 0.85f),
        ),
        scale = ClickableSurfaceDefaults.scale(focusedScale = 1.08f),
        border = ClickableSurfaceDefaults.border(focusedBorder = Border(BorderStroke(2.dp, NfColors.Focus), inset = (-3).dp, shape = shape)),
    ) {
        Row(
            modifier = Modifier.padding(horizontal = padding).align(Alignment.Center),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.Center,
        ) {
            if (icon != null) {
                Icon(icon, contentDescription = null, modifier = Modifier.size(17.dp))
                Spacer(Modifier.width(7.dp))
            }
            Text(text, style = NfType.Button, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
}

/** Round icon button (thumbs, My List): outlined circle, white when focused, lighter when [active]. */
@Composable
fun RoundIconButton(
    onClick: () -> Unit,
    contentDescription: String?,
    modifier: Modifier = Modifier,
    icon: ImageVector? = null,
    active: Boolean = false,
    content: (@Composable () -> Unit)? = null,
) {
    Surface(
        onClick = onClick,
        modifier = modifier.size(Dimens.ButtonHeight),
        shape = ClickableSurfaceDefaults.shape(CircleShape),
        colors = ClickableSurfaceDefaults.colors(
            containerColor = if (active) Color.White.copy(alpha = 0.25f) else Color(0x992A2A2A),
            contentColor = Color.White,
            focusedContainerColor = Color.White,
            focusedContentColor = Color.Black,
        ),
        scale = ClickableSurfaceDefaults.scale(focusedScale = 1.15f),
        border = ClickableSurfaceDefaults.border(
            border = Border(BorderStroke(1.6.dp, if (active) Color.White else Color.White.copy(alpha = 0.55f)), shape = CircleShape),
            focusedBorder = Border(BorderStroke(1.6.dp, Color.White), shape = CircleShape),
        ),
    ) {
        Box(Modifier.align(Alignment.Center).size(17.dp), contentAlignment = Alignment.Center) {
            when {
                content != null -> content()
                icon != null -> Icon(icon, contentDescription = contentDescription, modifier = Modifier.size(17.dp))
            }
        }
    }
}

/** Pill-shaped filter / option chip; [selected] shows it brighter. */
@Composable
fun NfChip(
    text: String,
    selected: Boolean,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    icon: ImageVector? = null,
) {
    Surface(
        onClick = onClick,
        modifier = modifier,
        shape = ClickableSurfaceDefaults.shape(RoundedCornerShape(16.dp)),
        colors = ClickableSurfaceDefaults.colors(
            containerColor = if (selected) Color.White.copy(alpha = 0.22f) else Color.White.copy(alpha = 0.1f),
            contentColor = if (selected) Color.White else NfColors.Muted,
            focusedContainerColor = Color.White,
            focusedContentColor = Color.Black,
        ),
        scale = ClickableSurfaceDefaults.scale(focusedScale = 1.06f),
    ) {
        Row(Modifier.padding(horizontal = 14.dp, vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
            if (icon != null) {
                Icon(icon, contentDescription = null, modifier = Modifier.size(14.dp))
                Spacer(Modifier.width(6.dp))
            }
            Text(text, style = NfType.Caption, maxLines = 1)
        }
    }
}
