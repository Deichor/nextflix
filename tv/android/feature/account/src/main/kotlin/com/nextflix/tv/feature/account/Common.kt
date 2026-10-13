package com.nextflix.tv.feature.account

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.tv.material3.ClickableSurfaceDefaults
import androidx.tv.material3.LocalContentColor
import androidx.tv.material3.Surface
import androidx.tv.material3.Text
import com.nextflix.tv.core.net.ApiException
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType
import java.io.InterruptedIOException

internal val OkGreen = Color(0xFF46D369)
internal val FailRed = Color(0xFFFF6B6B)
internal val WarnOrange = Color(0xFFE87C03)

/** Short reason of a failed call for status lines, in the profile language (only the server's error text is kept as sent). */
internal fun Throwable.describe(s: AccountStrings): String = when (this) {
    is ApiException.Http -> message.orEmpty()
    is ApiException.Network -> if (cause is InterruptedIOException) s.errorTimeout else s.errorUnreachable
    is ApiException.NoServer -> s.errorUnreachable
    is ApiException.Decode -> s.errorBadAddress
    else -> s.errorUnknown
}

/**
 * One settings row (webOS .setting): name on the left, value on the right; white with black text when focused.
 * [hint] adds a second, smaller line under the name.
 */
@Composable
internal fun SettingRow(
    name: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    value: String? = null,
    hint: String? = null,
    enabled: Boolean = true,
    valueColor: Color? = null,
) {
    Surface(
        onClick = onClick,
        enabled = enabled,
        modifier = modifier.width(SettingWidth).heightIn(min = 51.dp),
        shape = ClickableSurfaceDefaults.shape(RoundedCornerShape(4.dp)),
        colors = ClickableSurfaceDefaults.colors(
            containerColor = Color.White.copy(alpha = 0.06f),
            contentColor = Color.White,
            focusedContainerColor = Color.White,
            focusedContentColor = Color.Black,
            disabledContainerColor = Color.White.copy(alpha = 0.03f),
            disabledContentColor = NfColors.Dim,
        ),
        scale = ClickableSurfaceDefaults.scale(focusedScale = 1f),
    ) {
        val focusedText = LocalContentColor.current == Color.Black
        Row(
            Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 10.dp).align(Alignment.CenterStart),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            Column(Modifier.weight(1f)) {
                Text(name, style = NfType.Body.copy(fontSize = 14.5.sp), maxLines = 1, overflow = TextOverflow.Ellipsis)
                if (hint != null) {
                    Text(
                        hint,
                        style = NfType.BodySmall,
                        color = if (focusedText) Color(0xFF444444) else NfColors.Dim,
                        modifier = Modifier.padding(top = 2.dp),
                    )
                }
            }
            if (value != null) {
                Text(
                    value,
                    style = NfType.Body.copy(fontSize = 14.5.sp),
                    color = when {
                        focusedText -> Color(0xFF333333)
                        valueColor != null -> valueColor
                        else -> NfColors.Muted
                    },
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.padding(start = 8.dp),
                )
            }
        }
    }
}

internal val SettingWidth = 640.dp

/** Section heading inside the settings list (webOS .settings-section). */
@Composable
internal fun SectionTitle(text: String, modifier: Modifier = Modifier) {
    Text(
        text,
        style = NfType.PageTitle.copy(fontSize = 17.sp, lineHeight = 22.sp),
        color = NfColors.Muted,
        modifier = modifier.width(SettingWidth).padding(top = 24.dp, bottom = 11.dp),
    )
}
