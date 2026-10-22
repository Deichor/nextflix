package com.nextflix.tv.feature.browse

import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.i18n.coreStrings
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.model.RecommendationRow
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.model.Title
import com.nextflix.tv.core.model.toTitle
import com.nextflix.tv.core.net.arr
import com.nextflix.tv.core.net.int
import com.nextflix.tv.core.net.obj
import com.nextflix.tv.core.net.str
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.intOrNull
import java.time.LocalDate
import java.time.format.DateTimeFormatter
import java.time.format.TextStyle
import java.time.temporal.ChronoUnit
import kotlin.random.Random

// Pure rules ported from webOS lib/catalog.ts, screens/HomeScreen.tsx and lib/discover.ts

/** Alternates the two lists, at most [max] titles. */
internal fun interleave(a: List<Title>, b: List<Title>, max: Int = 20): List<Title> {
    val out = ArrayList<Title>(max)
    var i = 0
    while (out.size < max && (i < a.size || i < b.size)) {
        if (i < a.size) out += a[i]
        if (i < b.size && out.size < max) out += b[i]
        i++
    }
    return out
}

/** The lists one after another, each title once. */
internal fun unique(vararg lists: List<Title>): List<Title> {
    val seen = HashSet<String>()
    return lists.flatMap { it }.filter { seen.add(it.key) }
}

/** The billboard title: [key] again when it is still a candidate, else a random one of the first good six. */
internal fun pickFeatured(pool: List<Title>, key: String?, random: Random = Random): Title? {
    val good = pool.filter { it.backdrop.isNotEmpty() && it.overview.length > 40 }.take(6)
    return good.firstOrNull { it.key == key } ?: good.randomOrNull(random)
}

/** The first [n] titles with their rank (Top 10 rows built from trending lists). */
internal fun ranked(list: List<Title>, n: Int = 10): List<Title> = list.take(n).mapIndexed { i, t -> t.copy(rank = i + 1) }

private val BECAUSE = Regex("^Because you (?:loved|liked) (.+)$")
private val BECAUSE_FAVORITE = Regex("^Because you added (.+) to Favorites$")

/** Server row title in the UI language (the server sends English titles). */
internal fun recommendationTitle(s: BrowseStrings, row: RecommendationRow): String {
    val name = (BECAUSE.find(row.title) ?: BECAUSE_FAVORITE.find(row.title))?.groupValues?.get(1)
    return when (row.reason) {
        "top_picks" -> s.topPicks
        "popular" -> s.trendingNow
        "because_loved" -> name?.let(s::becauseLoved) ?: row.title
        "because_liked" -> name?.let(s::becauseLiked) ?: row.title
        "because_favorite" -> name?.let(s::becauseFavorite) ?: row.title
        else -> if (row.id == "trending") s.trendingNow else row.title
    }
}

// ---- Surprise me ----

internal enum class SurpriseKind(val apiName: String) {
    BecauseLoved("because_loved"),
    BecauseLiked("because_liked"),
    BecauseWatched("because_watched"),
    FavoriteLike("favorite_like"),
    Genre("genre"),
    Adjacent("adjacent"),
    WildCard("wild_card"),
    TrendingPick("trending_pick");

    companion object {
        fun of(value: String?): SurpriseKind = entries.firstOrNull { it.apiName == value } ?: TrendingPick
    }
}

internal data class SurpriseReason(
    val kind: SurpriseKind,
    val title: String? = null,
    val mediaType: MediaType? = null,
    val genreId: Int? = null,
    val genreIds: List<Int> = emptyList(),
    val language: String? = null,
)

internal data class SurprisePick(val title: Title, val logo: String, val reason: SurpriseReason)

/** GET /api/recommendations/surprise answer -> pick; null without a usable title. */
internal fun JsonElement.toSurprisePick(): SurprisePick? {
    val root = obj ?: return null
    val raw = root["title"]
    val title = raw.toTitle(MediaType.Movie) ?: return null
    val r = root.obj("reason")
    return SurprisePick(
        title = title,
        logo = raw.obj?.str("logo_path").orEmpty(),
        reason = SurpriseReason(
            kind = SurpriseKind.of(r?.str("kind")),
            title = r?.str("title")?.ifEmpty { null },
            mediaType = MediaType.of(r?.str("media_type")),
            genreId = r?.int("genre_id"),
            genreIds = r?.arr("genre_ids")?.mapNotNull { (it as? JsonPrimitive)?.intOrNull }.orEmpty(),
            language = r?.str("language"),
        ),
    )
}

/** Turkish locative suffix with vowel harmony: "Bilim kurgu" -> "Bilim kurguda", "Suç" -> "Suçta". */
internal fun trLocative(word: String): String {
    val lower = word.lowercase(AppLanguage.Tr.locale)
    var back = true
    for (c in lower.reversed()) {
        if (c in "aıouâû") break
        if (c in "eiöüî") {
            back = false
            break
        }
    }
    val hard = lower.lastOrNull()?.let { it in "çfhkpsşt" } ?: false
    return word + (if (hard) "t" else "d") + (if (back) "a" else "e")
}

/** "Because you loved Dune" / "Bayıldığın Dune filmine benzediği için", "Something completely different – ...". */
internal fun reasonText(s: BrowseStrings, language: AppLanguage, pick: SurprisePick): String {
    val reason = pick.reason
    val genreName = coreStrings[language]::genre
    val locale = language.locale
    fun lower(v: String) = v.lowercase(locale)
    // English names genres in lower case; Turkish starts the sentence with the first one
    fun lead(v: String) = if (language == AppLanguage.Tr) v.take(1).uppercase(locale) + lower(v.drop(1)) else lower(v)

    val seed = reason.title
    val tv = reason.mediaType == MediaType.Tv
    if (seed != null) {
        when (reason.kind) {
            SurpriseKind.BecauseLoved -> return s.reasonLoved(seed, tv)
            SurpriseKind.BecauseLiked -> return s.reasonLiked(seed, tv)
            SurpriseKind.BecauseWatched -> return s.reasonWatched(seed, tv)
            SurpriseKind.FavoriteLike -> return s.reasonFavorite(seed, tv)
            else -> Unit
        }
    }
    when (reason.kind) {
        SurpriseKind.Genre -> {
            val ids = reason.genreIds.ifEmpty { listOfNotNull(reason.genreId) }
            val names = ids.map(genreName).filter { it.isNotEmpty() }
            if (names.size >= 2) return s.reasonGenres(lead(names[0]), lower(names[1]))
            if (names.size == 1) return s.reasonGenre(lead(names[0]))
        }
        SurpriseKind.Adjacent -> {
            val genre = reason.genreId?.let(genreName).orEmpty()
            if (genre.isNotEmpty()) return s.reasonAdjacent(lower(genre), trLocative(lead(genre)))
        }
        SurpriseKind.WildCard -> {
            if (reason.language == "ja" && 16 in pick.title.genreIds) return s.reasonWildAnime
            val name = reason.language?.let(s::language)
            if (name != null) return if (pick.title.type == MediaType.Tv) s.reasonWildTv(name) else s.reasonWildMovie(name)
            return s.reasonWild
        }
        else -> Unit
    }
    return s.reasonTrending
}

/**
 * "New episode Thursday" for a Continue Watching entry not out yet: the weekday within the next 6 days, else day
 * and month. Null for a normal entry.
 */
internal fun upcomingLabel(p: StreamProgress, s: BrowseStrings, language: AppLanguage, today: LocalDate = LocalDate.now()): String? {
    val raw = p.nextAirDate ?: return null
    val date = runCatching { LocalDate.parse(raw.take(10)) }.getOrNull() ?: return s.newEpisode(raw)
    val days = ChronoUnit.DAYS.between(today, date)
    val day = if (days in 0..6) {
        date.dayOfWeek.getDisplayName(TextStyle.FULL, language.locale)
    } else {
        date.format(DateTimeFormatter.ofPattern(s.dayMonthPattern, language.locale))
    }
    return s.newEpisode(day)
}
