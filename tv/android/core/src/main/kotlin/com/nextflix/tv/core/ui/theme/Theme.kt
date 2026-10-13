package com.nextflix.tv.core.ui.theme

import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.Font
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.em
import androidx.compose.ui.unit.sp
import androidx.tv.material3.LocalContentColor
import androidx.tv.material3.MaterialTheme
import androidx.tv.material3.Typography
import androidx.tv.material3.darkColorScheme
import com.nextflix.tv.core.R

// 10-foot look of the webOS app (tv/webos/src/styles.css). The CSS is laid out for 1920x1080 with 1rem = 16px;
// a 1080p TV is 960x540 dp, so 1rem = 8 dp here.

object NfColors {
    val Background = Color(0xFF141414)
    val Red = Color(0xFFE50914)
    val RedDark = Color(0xFFB1060F)
    val Text = Color.White
    val TextSoft = Color(0xFFE5E5E5)
    val Muted = Color(0xFFB3B3B3)
    val Dim = Color(0xFF808080)
    val Card = Color(0xFF2F2F2F)
    val Skeleton = Color(0xFF2B2B2B)
    val Focus = Color.White
    /** secondary buttons: rgba(109,109,110,.7) */
    val Secondary = Color(0xB36D6D6E)
    val Progress = Red
    val ProgressTrack = Color(0xE65A5A5A)
    val Scrim = Color(0xD9000000)
}

object Dimens {
    /** left / right screen padding (webOS --pad 8rem), clear of the side menu and of overscan */
    val ScreenPadH = 64.dp
    /** top padding (overscan safe) */
    val ScreenPadTop = 27.dp
    /** side menu: closed / open width */
    val RailClosed = 52.dp
    val RailOpen = 192.dp
    val CardGap = 8.dp
    val CardRadius = 4.dp
    val BackdropCardWidth = 192.dp
    val BackdropCardHeight = 108.dp
    val PosterCardWidth = 104.dp
    val PosterCardHeight = 156.dp
    val ButtonHeight = 35.dp
    val FocusBorder = 3.dp
    val RowTitleGap = 4.dp
}

val Inter = FontFamily(
    Font(R.font.inter_regular, FontWeight.Normal),
    Font(R.font.inter_semibold, FontWeight.SemiBold),
    Font(R.font.inter_extrabold, FontWeight.ExtraBold),
)

/** Text styles (sp, Inter). Use MaterialTheme.typography in screens; [NfType] for the brand-specific ones. */
object NfType {
    /** NEXTFLIX wordmark */
    val Brand = TextStyle(fontFamily = Inter, fontWeight = FontWeight.ExtraBold, fontSize = 26.sp, letterSpacing = 0.08.em, color = NfColors.Red)
    /** hero / detail title */
    val Display = TextStyle(fontFamily = Inter, fontWeight = FontWeight.ExtraBold, fontSize = 36.sp, lineHeight = 40.sp)
    /** page titles (Settings, Setup, Who's watching) */
    val PageTitle = TextStyle(fontFamily = Inter, fontWeight = FontWeight.ExtraBold, fontSize = 27.sp, lineHeight = 32.sp)
    val RowTitle = TextStyle(fontFamily = Inter, fontWeight = FontWeight.SemiBold, fontSize = 15.sp, color = NfColors.TextSoft)
    val Body = TextStyle(fontFamily = Inter, fontWeight = FontWeight.Normal, fontSize = 14.sp, lineHeight = 20.sp)
    val BodySmall = TextStyle(fontFamily = Inter, fontWeight = FontWeight.Normal, fontSize = 12.sp, lineHeight = 16.sp)
    val Button = TextStyle(fontFamily = Inter, fontWeight = FontWeight.SemiBold, fontSize = 14.sp)
    val Caption = TextStyle(fontFamily = Inter, fontWeight = FontWeight.SemiBold, fontSize = 12.sp, lineHeight = 14.sp)
    val Fact = TextStyle(fontFamily = Inter, fontWeight = FontWeight.SemiBold, fontSize = 13.sp, color = NfColors.TextSoft)
}

private val typography = Typography(
    displayLarge = NfType.Display.copy(fontSize = 44.sp, lineHeight = 48.sp),
    displayMedium = NfType.Display,
    displaySmall = NfType.Display.copy(fontSize = 30.sp, lineHeight = 34.sp),
    headlineLarge = NfType.PageTitle.copy(fontSize = 30.sp),
    headlineMedium = NfType.PageTitle,
    headlineSmall = NfType.PageTitle.copy(fontSize = 22.sp, lineHeight = 28.sp),
    titleLarge = TextStyle(fontFamily = Inter, fontWeight = FontWeight.SemiBold, fontSize = 18.sp, lineHeight = 22.sp),
    titleMedium = TextStyle(fontFamily = Inter, fontWeight = FontWeight.SemiBold, fontSize = 15.sp, lineHeight = 20.sp),
    titleSmall = TextStyle(fontFamily = Inter, fontWeight = FontWeight.SemiBold, fontSize = 13.sp, lineHeight = 18.sp),
    bodyLarge = NfType.Body.copy(fontSize = 15.sp, lineHeight = 22.sp),
    bodyMedium = NfType.Body,
    bodySmall = NfType.BodySmall,
    labelLarge = NfType.Button,
    labelMedium = NfType.Caption,
    labelSmall = NfType.Caption.copy(fontSize = 10.sp),
)

private val colors = darkColorScheme(
    primary = Color.White,
    onPrimary = Color.Black,
    secondary = NfColors.Secondary,
    onSecondary = Color.White,
    background = NfColors.Background,
    onBackground = Color.White,
    surface = NfColors.Background,
    onSurface = Color.White,
    surfaceVariant = NfColors.Card,
    onSurfaceVariant = NfColors.Muted,
    error = NfColors.Red,
    border = NfColors.Focus,
)

@Composable
fun NextflixTheme(content: @Composable () -> Unit) {
    MaterialTheme(colorScheme = colors, typography = typography) {
        CompositionLocalProvider(LocalContentColor provides Color.White, content = content)
    }
}
