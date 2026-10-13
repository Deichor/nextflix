package com.nextflix.tv.core.i18n

import androidx.compose.runtime.Composable
import androidx.compose.runtime.ReadOnlyComposable

/** Strings used by more than one module. Module-only strings live in the module's own table. */
interface CoreStrings {
    val cancel: String
    val close: String
    val retry: String
    val save: String
    val saving: String
    val loading: String
    val back: String
    val ok: String
    val all: String
    val on: String
    val off: String
    val movies: String
    val tvShows: String

    val navHome: String
    val navSearch: String
    val navTvShows: String
    val navMovies: String
    val navSettings: String
    val navSurprise: String

    val exitTitle: String
    val exitConfirm: String

    val play: String
    val resume: String
    val moreInfo: String
    val comingSoon: String
    val instant: String

    val keyboardSpace: String
    val keyboardDelete: String
    val keyboardClear: String
    val keyboardShift: String
    val keyboardVoice: String

    val errorNoServer: String
    val errorNetwork: String
    val errorGeneric: String

    val inTheaters: String
    val episodeNotReleased: String
    val titleNotReleased: String
    fun comingOn(date: String): String
    fun inTheatersUntil(date: String): String
    fun airsOn(date: String): String

    fun match(percent: Int): String
    /** "S1:E2" / "S1:B2" */
    fun episodeShort(season: Int, episode: Int): String
    fun episodeN(episode: Int): String
    fun timeLeft(time: String): String
    fun seasons(count: Int): String
    fun durationHM(hours: Int, minutes: Int): String
    fun durationH(hours: Int): String
    fun durationM(minutes: Int): String
    /** TMDB genre name ("" when unknown) */
    fun genre(id: Int): String
}

private object CoreEn : CoreStrings {
    override val cancel = "Cancel"
    override val close = "Close"
    override val retry = "Retry"
    override val save = "Save"
    override val saving = "Saving..."
    override val loading = "Loading..."
    override val back = "Back"
    override val ok = "OK"
    override val all = "All"
    override val on = "On"
    override val off = "Off"
    override val movies = "Movies"
    override val tvShows = "TV Shows"
    override val navHome = "Home"
    override val navSearch = "Search"
    override val navTvShows = "TV Shows"
    override val navMovies = "Movies"
    override val navSettings = "Settings"
    override val navSurprise = "Surprise me"
    override val exitTitle = "Do you want to exit Nextflix?"
    override val exitConfirm = "Exit"
    override val play = "Play"
    override val resume = "Resume"
    override val moreInfo = "More Info"
    override val comingSoon = "Coming Soon"
    override val instant = "Instant"
    override val keyboardSpace = "Space"
    override val keyboardDelete = "Delete"
    override val keyboardClear = "Clear"
    override val keyboardShift = "Shift"
    override val keyboardVoice = "Voice search"
    override val errorNoServer = "No Nextflix server is set up."
    override val errorNetwork = "Could not reach the Nextflix server."
    override val errorGeneric = "Something went wrong."
    override val inTheaters = "In theaters"
    override val episodeNotReleased = "This episode hasn't been released yet"
    override val titleNotReleased = "This title isn't available to stream yet"
    override fun comingOn(date: String) = "Coming $date"
    override fun inTheatersUntil(date: String) = "In theaters · Streaming from $date"
    override fun airsOn(date: String) = "Airs $date"
    override fun match(percent: Int) = "$percent% Match"
    override fun episodeShort(season: Int, episode: Int) = "S$season:E$episode"
    override fun episodeN(episode: Int) = "Episode $episode"
    override fun timeLeft(time: String) = "$time left"
    override fun seasons(count: Int) = if (count == 1) "1 Season" else "$count Seasons"
    override fun durationHM(hours: Int, minutes: Int) = "${hours}h ${minutes}m"
    override fun durationH(hours: Int) = "${hours}h"
    override fun durationM(minutes: Int) = "${minutes}m"
    override fun genre(id: Int) = EnGenres[id].orEmpty()
}

private object CoreTr : CoreStrings {
    override val cancel = "İptal"
    override val close = "Kapat"
    override val retry = "Tekrar dene"
    override val save = "Kaydet"
    override val saving = "Kaydediliyor..."
    override val loading = "Yükleniyor..."
    override val back = "Geri"
    override val ok = "Tamam"
    override val all = "Tümü"
    override val on = "Açık"
    override val off = "Kapalı"
    override val movies = "Filmler"
    override val tvShows = "Diziler"
    override val navHome = "Ana Sayfa"
    override val navSearch = "Ara"
    override val navTvShows = "Diziler"
    override val navMovies = "Filmler"
    override val navSettings = "Ayarlar"
    override val navSurprise = "Beni şaşırt"
    override val exitTitle = "Nextflix'ten çıkmak istiyor musun?"
    override val exitConfirm = "Çık"
    override val play = "Oynat"
    override val resume = "Devam Et"
    override val moreInfo = "Daha Fazla Bilgi"
    override val comingSoon = "Çok Yakında"
    override val instant = "Anında açılır"
    override val keyboardSpace = "Boşluk"
    override val keyboardDelete = "Sil"
    override val keyboardClear = "Temizle"
    override val keyboardShift = "Büyük harf"
    override val keyboardVoice = "Sesli arama"
    override val errorNoServer = "Nextflix sunucusu ayarlanmamış."
    override val errorNetwork = "Nextflix sunucusuna ulaşılamadı."
    override val errorGeneric = "Bir şeyler ters gitti."
    override val inTheaters = "Sinemalarda"
    override val episodeNotReleased = "Bu bölüm henüz yayınlanmadı"
    override val titleNotReleased = "Bu içerik henüz dijital olarak yayınlanmadı"
    override fun comingOn(date: String) = "$date tarihinde yayında"
    override fun inTheatersUntil(date: String) = "Sinemalarda · Dijital çıkış: $date"
    override fun airsOn(date: String) = "$date yayında"
    override fun match(percent: Int) = "%$percent Eşleşme"
    override fun episodeShort(season: Int, episode: Int) = "S$season:B$episode"
    override fun episodeN(episode: Int) = "$episode. Bölüm"
    override fun timeLeft(time: String) = "$time kaldı"
    override fun seasons(count: Int) = "$count Sezon"
    override fun durationHM(hours: Int, minutes: Int) = "$hours sa $minutes dk"
    override fun durationH(hours: Int) = "$hours sa"
    override fun durationM(minutes: Int) = "$minutes dk"
    override fun genre(id: Int) = TrGenres[id].orEmpty()
}

val coreStrings = Localized<CoreStrings>(en = CoreEn, tr = CoreTr)

/** Shared strings in the active profile's language. */
@Composable
@ReadOnlyComposable
fun strings(): CoreStrings = coreStrings.current()

private val EnGenres = mapOf(
    28 to "Action", 12 to "Adventure", 16 to "Animation", 35 to "Comedy", 80 to "Crime", 99 to "Documentary",
    18 to "Drama", 10751 to "Family", 14 to "Fantasy", 36 to "History", 27 to "Horror", 10402 to "Music",
    9648 to "Mystery", 10749 to "Romance", 878 to "Sci-Fi", 53 to "Thriller", 10752 to "War", 37 to "Western",
    10759 to "Action & Adventure", 10762 to "Kids", 10764 to "Reality", 10765 to "Sci-Fi & Fantasy",
    10768 to "War & Politics",
)

private val TrGenres = mapOf(
    28 to "Aksiyon", 12 to "Macera", 16 to "Animasyon", 35 to "Komedi", 80 to "Suç", 99 to "Belgesel",
    18 to "Dram", 10751 to "Aile", 14 to "Fantastik", 36 to "Tarih", 27 to "Korku", 10402 to "Müzik",
    9648 to "Gizem", 10749 to "Romantik", 878 to "Bilim Kurgu", 53 to "Gerilim", 10752 to "Savaş", 37 to "Western",
    10759 to "Aksiyon ve Macera", 10762 to "Çocuk", 10764 to "Reality", 10765 to "Bilim Kurgu ve Fantastik",
    10768 to "Savaş ve Politika",
)
