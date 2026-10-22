package com.nextflix.tv.feature.browse

import com.nextflix.tv.core.i18n.Localized

// Texts from tv/webos/src/lib/i18n.ts (home.*, catalog.*, favorites.title, surprise.*, genre.*)
internal interface BrowseStrings {
    fun continueWatching(name: String): String
    val trendingNow: String
    val top10Movies: String
    val top10Tv: String
    val top10TrMovies: String
    val top10TrTv: String
    val newEpisodes: String
    val comingSoon: String
    val popularOnNextflix: String
    val newReleases: String
    val popularMovies: String
    val popularTv: String
    val nowPlaying: String
    val currentlyAiring: String
    val topPicks: String
    fun becauseLoved(title: String): String
    fun becauseLiked(title: String): String
    fun becauseFavorite(title: String): String
    val genreActionAdventure: String
    val genreComedies: String
    val genreSciFiFantasy: String
    val genreHorror: String
    val genreAcclaimedMovies: String
    val providerNetflix: String
    val providerDisney: String
    val providerMax: String
    val acclaimedTv: String
    val kickerFilm: String
    val kickerSeries: String
    val favorites: String
    /** Continue Watching card of an episode not out yet; [day] "Thursday" / "October 29" */
    fun newEpisode(day: String): String
    /** [java.time.format.DateTimeFormatter] pattern of a day and month: "October 29" / "29 Ekim" */
    val dayMonthPattern: String

    val surpriseHeading: String
    val surpriseNext: String
    val surpriseDetails: String
    val surpriseError: String
    val surpriseHint: String
    fun reasonLoved(title: String, tv: Boolean): String
    fun reasonLiked(title: String, tv: Boolean): String
    fun reasonWatched(title: String, tv: Boolean): String
    fun reasonFavorite(title: String, tv: Boolean): String
    fun reasonGenre(genre: String): String
    fun reasonGenres(first: String, second: String): String
    /** [genreIn]: Turkish locative form of the genre ("Bilim kurguda") */
    fun reasonAdjacent(genre: String, genreIn: String): String
    val reasonWild: String
    fun reasonWildMovie(language: String): String
    fun reasonWildTv(language: String): String
    val reasonWildAnime: String
    val reasonTrending: String
    /** Adjective for a wild card's original language ("Korean"); null for languages without a text. */
    fun language(code: String): String?
}

internal object BrowseEn : BrowseStrings {
    override fun continueWatching(name: String) = "Continue Watching for $name"
    override val trendingNow = "Trending Now"
    override val top10Movies = "Top 10 Movies Today"
    override val top10Tv = "Top 10 TV Shows Today"
    override val top10TrMovies = "Top 10 Movies in Türkiye This Week"
    override val top10TrTv = "Top 10 TV Shows in Türkiye This Week"
    override val newEpisodes = "New Episodes"
    override val comingSoon = "Coming Soon"
    override val popularOnNextflix = "Popular on Nextflix"
    override val newReleases = "New Releases"
    override val popularMovies = "Popular Movies"
    override val popularTv = "Popular TV Shows"
    override val nowPlaying = "Now Playing in Theaters"
    override val currentlyAiring = "Currently Airing"
    override val topPicks = "Top Picks for You"
    override fun becauseLoved(title: String) = "Because you loved $title"
    override fun becauseLiked(title: String) = "Because you liked $title"
    override fun becauseFavorite(title: String) = "Because you added $title to Favorites"
    override val genreActionAdventure = "Action & Adventure Movies"
    override val genreComedies = "Comedies"
    override val genreSciFiFantasy = "Sci-Fi & Fantasy"
    override val genreHorror = "Horror Movies"
    override val genreAcclaimedMovies = "Critically Acclaimed Movies"
    override val providerNetflix = "Popular on Netflix"
    override val providerDisney = "Disney+ Favorites"
    override val providerMax = "Max Originals & More"
    override val acclaimedTv = "Critically Acclaimed TV"
    override val kickerFilm = "FILM"
    override val kickerSeries = "SERIES"
    override val favorites = "Favorites"
    override fun newEpisode(day: String) = "New episode $day"
    override val dayMonthPattern = "MMMM d"

    override val surpriseHeading = "How about this?"
    override val surpriseNext = "Something else"
    override val surpriseDetails = "Details"
    override val surpriseError = "Could not find a title right now."
    override val surpriseHint =
        "One pick at a time: close to your taste, a new find in genres you like, or something completely different."
    override fun reasonLoved(title: String, tv: Boolean) = "Because you loved $title"
    override fun reasonLiked(title: String, tv: Boolean) = "Because you liked $title"
    override fun reasonWatched(title: String, tv: Boolean) = "Because you watched $title"
    override fun reasonFavorite(title: String, tv: Boolean) = "Like $title from your Favorites"
    override fun reasonGenre(genre: String) = "Because you like $genre"
    override fun reasonGenres(first: String, second: String) = "Because you like $first and $second"
    override fun reasonAdjacent(genre: String, genreIn: String) = "A new pick in $genre"
    override val reasonWild = "Something completely different"
    override fun reasonWildMovie(language: String) = "Something completely different – a highly rated $language film"
    override fun reasonWildTv(language: String) = "Something completely different – a highly rated $language series"
    override val reasonWildAnime = "Something completely different – a highly rated anime"
    override val reasonTrending = "Trending this week"

    private val languages = mapOf(
        "ko" to "Korean", "ja" to "Japanese", "es" to "Spanish", "fr" to "French", "it" to "Italian",
        "tr" to "Turkish", "hi" to "Indian", "de" to "German", "zh" to "Chinese", "da" to "Danish",
        "sv" to "Swedish", "pt" to "Portuguese", "ru" to "Russian", "fa" to "Iranian",
    )
    override fun language(code: String) = languages[code]
}

internal object BrowseTr : BrowseStrings {
    override fun continueWatching(name: String) = "$name için İzlemeye Devam Et"
    override val trendingNow = "Gündemdekiler"
    override val top10Movies = "Bugünün En Popüler 10 Filmi"
    override val top10Tv = "Bugünün En Popüler 10 Dizisi"
    override val top10TrMovies = "Türkiye'de Bu Hafta Top 10 Film"
    override val top10TrTv = "Türkiye'de Bu Hafta Top 10 Dizi"
    override val newEpisodes = "Yeni Bölümler"
    override val comingSoon = "Çok Yakında"
    override val popularOnNextflix = "Nextflix'te Popüler"
    override val newReleases = "Yeni Çıkanlar"
    override val popularMovies = "Popüler Filmler"
    override val popularTv = "Popüler Diziler"
    override val nowPlaying = "Vizyondakiler"
    override val currentlyAiring = "Yayında Olanlar"
    override val topPicks = "Senin İçin En İyi Seçimler"
    override fun becauseLoved(title: String) = "$title yapımına bayıldığın için"
    override fun becauseLiked(title: String) = "$title yapımını beğendiğin için"
    override fun becauseFavorite(title: String) = "$title yapımını favorilerine eklediğin için"
    override val genreActionAdventure = "Aksiyon ve Macera Filmleri"
    override val genreComedies = "Komediler"
    override val genreSciFiFantasy = "Bilim Kurgu ve Fantastik"
    override val genreHorror = "Korku Filmleri"
    override val genreAcclaimedMovies = "Eleştirmenlerin Beğendiği Filmler"
    override val providerNetflix = "Netflix'te Popüler"
    override val providerDisney = "Disney+ Favorileri"
    override val providerMax = "Max Orijinalleri ve Fazlası"
    override val acclaimedTv = "Eleştirmenlerin Beğendiği Diziler"
    override val kickerFilm = "FİLM"
    override val kickerSeries = "DİZİ"
    override val favorites = "Favoriler"
    override fun newEpisode(day: String) = "Yeni bölüm $day"
    override val dayMonthPattern = "d MMMM"

    override val surpriseHeading = "Buna ne dersin?"
    override val surpriseNext = "Başka bir şey"
    override val surpriseDetails = "Ayrıntılar"
    override val surpriseError = "Şu an bir yapım bulunamadı."
    override val surpriseHint =
        "Her seferinde tek bir öneri: zevkine yakın bir yapım, sevdiğin türlerde yeni bir keşif ya da bambaşka bir şey."
    private fun kind(tv: Boolean) = if (tv) "dizisine" else "filmine"
    override fun reasonLoved(title: String, tv: Boolean) = "Bayıldığın $title ${kind(tv)} benzediği için"
    override fun reasonLiked(title: String, tv: Boolean) = "Beğendiğin $title ${kind(tv)} benzediği için"
    override fun reasonWatched(title: String, tv: Boolean) = "İzlediğin $title ${kind(tv)} benzediği için"
    override fun reasonFavorite(title: String, tv: Boolean) = "Favorilerindeki $title ${kind(tv)} benzediği için"
    override fun reasonGenre(genre: String) = "$genre sevdiğin için"
    override fun reasonGenres(first: String, second: String) = "$first ve $second sevdiğin için"
    override fun reasonAdjacent(genre: String, genreIn: String) = "$genreIn yeni bir keşif"
    override val reasonWild = "Bambaşka bir şey"
    override fun reasonWildMovie(language: String) = "Bambaşka bir şey – çok beğenilen bir $language filmi"
    override fun reasonWildTv(language: String) = "Bambaşka bir şey – çok beğenilen bir $language dizisi"
    override val reasonWildAnime = "Bambaşka bir şey – çok beğenilen bir anime"
    override val reasonTrending = "Bu hafta çok izleniyor"

    private val languages = mapOf(
        "ko" to "Kore", "ja" to "Japon", "es" to "İspanyol", "fr" to "Fransız", "it" to "İtalyan",
        "tr" to "Türk", "hi" to "Hint", "de" to "Alman", "zh" to "Çin", "da" to "Danimarka",
        "sv" to "İsveç", "pt" to "Portekiz", "ru" to "Rus", "fa" to "İran",
    )
    override fun language(code: String) = languages[code]
}

internal val browseStrings = Localized<BrowseStrings>(en = BrowseEn, tr = BrowseTr)
