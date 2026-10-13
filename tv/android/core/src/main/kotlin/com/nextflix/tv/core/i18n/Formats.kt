package com.nextflix.tv.core.i18n

import java.time.LocalDate
import java.time.OffsetDateTime
import java.time.format.DateTimeFormatter
import java.time.format.FormatStyle
import kotlin.math.max
import kotlin.math.roundToInt

/** Locale-aware formatting that follows the profile language (not the device locale). */
object Formats {
    /** Runtime in minutes as "1h 36m" / "1 sa 36 dk". */
    fun duration(language: AppLanguage, minutes: Int): String {
        val total = max(0, minutes)
        val h = total / 60
        val m = total % 60
        val s = coreStrings[language]
        return when {
            h > 0 && m > 0 -> s.durationHM(h, m)
            h > 0 -> s.durationH(h)
            else -> s.durationM(m)
        }
    }

    /** "2026-10-13" (or an ISO date-time) as "October 13, 2026" / "13 Ekim 2026"; "" when unparsable. */
    fun date(language: AppLanguage, date: String): String {
        val d = runCatching { LocalDate.parse(date.take(10)) }.getOrNull()
            ?: runCatching { OffsetDateTime.parse(date).toLocalDate() }.getOrNull()
            ?: return ""
        return d.format(DateTimeFormatter.ofLocalizedDate(FormatStyle.LONG).withLocale(language.locale))
    }

    /** "S1:E2" / "S1:B2", or "" without both numbers. */
    fun episode(language: AppLanguage, season: Int?, episode: Int?): String =
        if (season != null && episode != null && season > 0 && episode > 0) {
            coreStrings[language].episodeShort(season, episode)
        } else {
            ""
        }

    /** Player clock: 75 -> "1:15", 3725 -> "1:02:05". */
    fun clock(seconds: Double): String {
        val t = max(0, seconds.roundToInt())
        val h = t / 3600
        val m = (t % 3600) / 60
        val s = t % 60
        return if (h > 0) "%d:%02d:%02d".format(h, m, s) else "%d:%02d".format(m, s)
    }
}
