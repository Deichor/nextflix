package com.nextflix.tv.core.i18n

import androidx.compose.runtime.Composable
import androidx.compose.runtime.ReadOnlyComposable
import androidx.compose.runtime.compositionLocalOf
import java.util.Locale

/** UI and content language. It follows the active profile (Profile.language), never the device locale. */
enum class AppLanguage(val code: String, val locale: Locale) {
    En("en", Locale.US),
    Tr("tr", Locale.forLanguageTag("tr-TR"));

    companion object {
        /** "tr" -> Tr, anything else (also null) -> En, like the server's default. */
        fun of(code: String?): AppLanguage = if (code.equals("tr", ignoreCase = true)) Tr else En
    }
}

/** The active profile's language; provided by the app root, changes when the profile changes. */
val LocalAppLanguage = compositionLocalOf { AppLanguage.En }

/**
 * One module's string table in both languages. Declare an interface with the strings, implement it once per
 * language (the compiler then checks that Turkish has every string) and read it with [current]:
 *
 * ```
 * internal interface BrowseStrings { val trendingNow: String; fun continueWatching(name: String): String }
 * internal val browseStrings = Localized<BrowseStrings>(en = BrowseEn, tr = BrowseTr)
 * // in a composable: val s = browseStrings.current()
 * ```
 */
class Localized<T>(private val en: T, private val tr: T) {
    operator fun get(language: AppLanguage): T = if (language == AppLanguage.Tr) tr else en

    @Composable
    @ReadOnlyComposable
    fun current(): T = get(LocalAppLanguage.current)
}

/** Turkish has no plural form after a number; English uses [one] for exactly 1. */
fun AppLanguage.plural(count: Int, one: String, other: String): String =
    if (this == AppLanguage.En && count == 1) one else other
