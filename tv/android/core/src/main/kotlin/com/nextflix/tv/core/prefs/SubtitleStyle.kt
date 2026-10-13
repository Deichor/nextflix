package com.nextflix.tv.core.prefs

import androidx.compose.runtime.Immutable
import androidx.compose.ui.graphics.Color
import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.i18n.Localized
import kotlinx.serialization.Serializable

// Subtitle appearance (webOS lib/subtitleStyle.ts), per profile in PlayerPrefs. Shared by the player panel
// (:feature:player / :feature:subtitles) and Settings (:feature:account), so it lives in core.

/** Text size as a factor of the medium size. */
enum class SubtitleSize(val scale: Float) { Small(0.8f), Medium(1f), Large(1.25f), XLarge(1.5f) }

/** Box behind each line: none, see-through (black 55 %), black. */
enum class SubtitleBackground(val boxColor: Color) {
    None(Color.Transparent),
    Semi(Color(0x8C000000)),
    Black(Color.Black),
}

/** Static gradient band over the bottom 40 % that fades in while cues show. */
enum class SubtitleDimming { None, Light, Dark }

/** Shadow (soft drop shadow), outline (eight hard 0.05 em shadows) or none. */
enum class SubtitleEdge { Shadow, Outline, None }

enum class SubtitleColor(val color: Color) {
    White(Color.White),
    Yellow(Color(0xFFFFE94D)),
    Cyan(Color(0xFF5EE8FF)),
}

/** Bottom, or a bit higher (16 % of the screen height from the bottom). */
enum class SubtitlePosition { Bottom, Raised }

@Immutable
@Serializable
data class SubtitleStyle(
    val size: SubtitleSize = SubtitleSize.Medium,
    val background: SubtitleBackground = SubtitleBackground.None,
    val dimming: SubtitleDimming = SubtitleDimming.None,
    val edge: SubtitleEdge = SubtitleEdge.Shadow,
    val color: SubtitleColor = SubtitleColor.White,
    val position: SubtitlePosition = SubtitlePosition.Bottom,
) {
    /** One row of the appearance list; [cycle] steps its value with wrap-around (Left / Right, OK). */
    enum class Option { Size, Background, Dimming, Edge, Color, Position }

    fun cycle(option: Option, step: Int = 1): SubtitleStyle = when (option) {
        Option.Size -> copy(size = size.step(step))
        Option.Background -> copy(background = background.step(step))
        Option.Dimming -> copy(dimming = dimming.step(step))
        Option.Edge -> copy(edge = edge.step(step))
        Option.Color -> copy(color = color.step(step))
        Option.Position -> copy(position = position.step(step))
    }

    /** Label of the current value of [option] in [language]. */
    fun valueLabel(option: Option, language: AppLanguage): String {
        val s = subtitleStyleStrings[language]
        return when (option) {
            Option.Size -> when (size) {
                SubtitleSize.Small -> s.sizeSmall
                SubtitleSize.Medium -> s.sizeMedium
                SubtitleSize.Large -> s.sizeLarge
                SubtitleSize.XLarge -> s.sizeXLarge
            }
            Option.Background -> when (background) {
                SubtitleBackground.None -> s.none
                SubtitleBackground.Semi -> s.bgSemi
                SubtitleBackground.Black -> s.bgBlack
            }
            Option.Dimming -> when (dimming) {
                SubtitleDimming.None -> s.none
                SubtitleDimming.Light -> s.windowLight
                SubtitleDimming.Dark -> s.windowDark
            }
            Option.Edge -> when (edge) {
                SubtitleEdge.Shadow -> s.edgeShadow
                SubtitleEdge.Outline -> s.edgeOutline
                SubtitleEdge.None -> s.none
            }
            Option.Color -> when (color) {
                SubtitleColor.White -> s.colorWhite
                SubtitleColor.Yellow -> s.colorYellow
                SubtitleColor.Cyan -> s.colorCyan
            }
            Option.Position -> when (position) {
                SubtitlePosition.Bottom -> s.posBottom
                SubtitlePosition.Raised -> s.posRaised
            }
        }
    }

    companion object {
        fun optionLabel(option: Option, language: AppLanguage): String {
            val s = subtitleStyleStrings[language]
            return when (option) {
                Option.Size -> s.size
                Option.Background -> s.bg
                Option.Dimming -> s.window
                Option.Edge -> s.edge
                Option.Color -> s.color
                Option.Position -> s.pos
            }
        }
    }
}

private inline fun <reified E : Enum<E>> E.step(step: Int): E {
    val values = enumValues<E>()
    return values[((ordinal + step) % values.size + values.size) % values.size]
}

interface SubtitleStyleStrings {
    val title: String
    val sample: String
    val size: String
    val sizeSmall: String
    val sizeMedium: String
    val sizeLarge: String
    val sizeXLarge: String
    val bg: String
    val bgSemi: String
    val bgBlack: String
    val window: String
    val windowLight: String
    val windowDark: String
    val edge: String
    val edgeShadow: String
    val edgeOutline: String
    val color: String
    val colorWhite: String
    val colorYellow: String
    val colorCyan: String
    val pos: String
    val posBottom: String
    val posRaised: String
    val none: String
    val reset: String
    val done: String
}

private object SubtitleStyleEn : SubtitleStyleStrings {
    override val title = "Subtitle appearance"
    override val sample = "This is how subtitles will look."
    override val size = "Size"
    override val sizeSmall = "Small"
    override val sizeMedium = "Medium"
    override val sizeLarge = "Large"
    override val sizeXLarge = "Extra large"
    override val bg = "Background"
    override val bgSemi = "See-through box"
    override val bgBlack = "Black box"
    override val window = "Dimming"
    override val windowLight = "Light"
    override val windowDark = "Dark"
    override val edge = "Edge"
    override val edgeShadow = "Shadow"
    override val edgeOutline = "Outline"
    override val color = "Color"
    override val colorWhite = "White"
    override val colorYellow = "Yellow"
    override val colorCyan = "Cyan"
    override val pos = "Position"
    override val posBottom = "Bottom"
    override val posRaised = "A bit higher"
    override val none = "None"
    override val reset = "Reset to default"
    override val done = "Done"
}

private object SubtitleStyleTr : SubtitleStyleStrings {
    override val title = "Altyazı görünümü"
    override val sample = "Altyazılar böyle görünecek."
    override val size = "Boyut"
    override val sizeSmall = "Küçük"
    override val sizeMedium = "Orta"
    override val sizeLarge = "Büyük"
    override val sizeXLarge = "Çok büyük"
    override val bg = "Arka plan"
    override val bgSemi = "Yarı saydam kutu"
    override val bgBlack = "Siyah kutu"
    override val window = "Karartma"
    override val windowLight = "Hafif"
    override val windowDark = "Koyu"
    override val edge = "Kenar"
    override val edgeShadow = "Gölge"
    override val edgeOutline = "Kontur"
    override val color = "Renk"
    override val colorWhite = "Beyaz"
    override val colorYellow = "Sarı"
    override val colorCyan = "Camgöbeği"
    override val pos = "Konum"
    override val posBottom = "Alt"
    override val posRaised = "Biraz yukarı"
    override val none = "Yok"
    override val reset = "Varsayılana dön"
    override val done = "Tamam"
}

val subtitleStyleStrings = Localized<SubtitleStyleStrings>(en = SubtitleStyleEn, tr = SubtitleStyleTr)
