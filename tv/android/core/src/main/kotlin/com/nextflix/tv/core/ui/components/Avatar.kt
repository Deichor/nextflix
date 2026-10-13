package com.nextflix.tv.core.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.tv.material3.Icon
import androidx.tv.material3.Text
import com.nextflix.tv.core.ui.icons.Lucide
import com.nextflix.tv.core.ui.theme.Inter

/** Avatar keys in picker order, the same as the web app (AVATAR_PRESETS). */
val AvatarKeys: List<String> = listOf(
    "smile", "star", "heart", "rocket", "ghost", "cat", "dog", "bird", "flame", "crown",
    "gamepad", "zap", "sun", "moon", "leaf", "snowflake", "popcorn", "pizza", "bot", "skull",
)

/** Profile colors, the same as the web app (PROFILE_COLORS). */
val ProfileColors: List<String> = listOf(
    "#E50914", "#0071EB", "#F5C518", "#46D369", "#9B5DE5", "#FF7A00", "#E1307C", "#00B8A9", "#6D6D6E", "#8C4A2F",
)

fun avatarIcon(key: String): ImageVector? = when (key) {
    "smile" -> Lucide.Smile
    "star" -> Lucide.Star
    "heart" -> Lucide.Heart
    "rocket" -> Lucide.Rocket
    "ghost" -> Lucide.Ghost
    "cat" -> Lucide.Cat
    "dog" -> Lucide.Dog
    "bird" -> Lucide.Bird
    "flame" -> Lucide.Flame
    "crown" -> Lucide.Crown
    "gamepad" -> Lucide.Gamepad2
    "zap" -> Lucide.Zap
    "sun" -> Lucide.Sun
    "moon" -> Lucide.Moon
    "leaf" -> Lucide.Leaf
    "snowflake" -> Lucide.Snowflake
    "popcorn" -> Lucide.Popcorn
    "pizza" -> Lucide.Pizza
    "bot" -> Lucide.Bot
    "skull" -> Lucide.Skull
    else -> null
}

/** "#E50914" -> Color; red for anything unparsable. */
fun profileColor(hex: String?): Color =
    hex?.removePrefix("#")?.takeIf { it.length == 6 }?.toLongOrNull(16)?.let { Color(0xFF000000 or it) } ?: Color(0xFFE50914)

/** Profile picture: the avatar icon on the profile color (initial letter for unknown avatars). */
@Composable
fun Avatar(avatar: String, color: String?, name: String, modifier: Modifier = Modifier, size: Dp = 64.dp) {
    Box(
        modifier
            .size(size)
            .background(profileColor(color), RoundedCornerShape(size * 0.08f)),
        contentAlignment = Alignment.Center,
    ) {
        val icon = avatarIcon(avatar)
        if (icon != null) {
            Icon(icon, contentDescription = null, tint = Color.White, modifier = Modifier.fillMaxSize().padding(size * 0.2f))
        } else {
            Text(
                name.firstOrNull()?.uppercase() ?: "?",
                color = Color.White,
                fontFamily = Inter,
                fontWeight = FontWeight.ExtraBold,
                fontSize = (size.value * 0.45f).sp,
            )
        }
    }
}
