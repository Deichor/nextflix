package com.nextflix.tv.feature.player

import com.nextflix.tv.core.i18n.Localized

/** Messages the engine sets; the UI turns them into text. */
internal enum class Msg {
    FindingSource, FindingComplete, TryingAnother, SearchingIndexers, CheckingSources,
    NoSourceHint, CouldNotRead, ConnectionLost, PlaybackFailed, Network,
    SwitchedHd, SwitchedFaster, SwitchedHigher, SwitchedTo4k, SwitchedSubtitles, SubtitleFound,
    Connecting, ReadingInfo, Buffering,
}

internal interface PlayerStrings {
    val play: String
    val pause: String
    val back10: String
    val forward10: String
    val scrubHint: String
    val audio: String
    fun audioN(n: Int): String
    val audioSubtitles: String
    val original: String
    val stereo: String
    val mono: String
    val subtitles: String
    val subtitlesOff: String
    val subtitlesSearching: String
    val subtitlesNone: String
    /** row offering the release that carries the profile language's subtitle */
    val subAltRow: String
    val subAltSwitching: String
    val subAi: String
    val aiTranslating: String
    val nextEpisode: String
    val back: String
    val youreWatching: String
    val paused: String
    val retry: String
    val stillSearching: String
    val couldNotStartEpisode: String
    val couldNotStartTitle: String
    fun nextStartsIn(label: String, n: Int): String
    val playNow: String
    val skipIntro: String
    val watchCredits: String
    val smoothBuffering: String
    val reconnecting: String
    val subtitleDelay: String
    val subtitleDelayHint: String
    fun seconds(value: String): String
    val cinemaBadge: String
    fun msg(m: Msg): String
}

private object PlayerEn : PlayerStrings {
    override val play = "Play"
    override val pause = "Pause"
    override val back10 = "Back 10s"
    override val forward10 = "Forward 10s"
    override val scrubHint = "OK: go · Back: cancel"
    override val audio = "Audio"
    override fun audioN(n: Int) = "Audio $n"
    override val audioSubtitles = "Audio & Subtitles"
    override val original = "Original"
    override val stereo = "Stereo"
    override val mono = "Mono"
    override val subtitles = "Subtitles"
    override val subtitlesOff = "Off"
    override val subtitlesSearching = "Looking for subtitles..."
    override val subtitlesNone = "No subtitles found"
    override val subAltRow = "English (another version) – switch"
    override val subAltSwitching = "Switching…"
    override val subAi = "AI translation"
    override val aiTranslating = "Translating subtitles with AI..."
    override val nextEpisode = "Next episode"
    override val back = "Back"
    override val youreWatching = "You're watching"
    override val paused = "Paused"
    override val retry = "Try again"
    override val stillSearching = "Still searching, this can take up to a minute."
    override val couldNotStartEpisode = "We couldn't start this episode"
    override val couldNotStartTitle = "We couldn't start this title"
    override fun nextStartsIn(label: String, n: Int) = "$label starts in $n"
    override val playNow = "Play Now"
    override val skipIntro = "Skip intro"
    override val watchCredits = "Watch credits"
    override val smoothBuffering = "Buffering for smooth playback…"
    override val reconnecting = "Reconnecting..."
    override val subtitleDelay = "Subtitle timing"
    override val subtitleDelayHint = "Left: earlier · Right: later · OK: reset"
    override fun seconds(value: String) = "$value s"
    override val cinemaBadge = "Cinema recording"
    override fun msg(m: Msg) = when (m) {
        Msg.FindingSource -> "Finding the best source..."
        Msg.FindingComplete -> "Finding a complete version..."
        Msg.TryingAnother -> "Trying another source..."
        Msg.SearchingIndexers -> "Searching indexers..."
        Msg.CheckingSources -> "Checking sources..."
        Msg.NoSourceHint -> "No playable source was found right now. Try again in a moment."
        Msg.CouldNotRead -> "Could not read the video"
        Msg.ConnectionLost -> "The connection to this torrent was lost."
        Msg.PlaybackFailed -> "Playback failed. The torrent may not have enough peers."
        Msg.Network -> "Could not reach the Nextflix server."
        Msg.SwitchedHd -> "Switched to HD for smoother playback"
        Msg.SwitchedFaster -> "Switched to a faster source"
        Msg.SwitchedHigher -> "Switched to higher quality"
        Msg.SwitchedTo4k -> "Switched to 4K"
        Msg.SwitchedSubtitles -> "Switched to the version with English subtitles"
        Msg.SubtitleFound -> "English subtitles found"
        Msg.Connecting -> "Connecting to peers..."
        Msg.ReadingInfo -> "Reading video info..."
        Msg.Buffering -> "Buffering..."
    }
}

private object PlayerTr : PlayerStrings {
    override val play = "Oynat"
    override val pause = "Duraklat"
    override val back10 = "10 sn geri"
    override val forward10 = "10 sn ileri"
    override val scrubHint = "OK: git · Geri: iptal"
    override val audio = "Ses"
    override fun audioN(n: Int) = "Ses $n"
    override val audioSubtitles = "Ses ve Altyazılar"
    override val original = "Orijinal"
    override val stereo = "Stereo"
    override val mono = "Mono"
    override val subtitles = "Altyazılar"
    override val subtitlesOff = "Kapalı"
    override val subtitlesSearching = "Altyazılar aranıyor..."
    override val subtitlesNone = "Altyazı bulunamadı"
    override val subAltRow = "Türkçe (başka sürümde) – geçiş yap"
    override val subAltSwitching = "Geçiliyor…"
    override val subAi = "AI çevirisi"
    override val aiTranslating = "Altyazı AI ile çevriliyor..."
    override val nextEpisode = "Sonraki bölüm"
    override val back = "Geri"
    override val youreWatching = "İzlediğiniz içerik"
    override val paused = "Duraklatıldı"
    override val retry = "Tekrar dene"
    override val stillSearching = "Hâlâ aranıyor, bu bir dakika kadar sürebilir."
    override val couldNotStartEpisode = "Bu bölüm başlatılamadı"
    override val couldNotStartTitle = "Bu içerik başlatılamadı"
    override fun nextStartsIn(label: String, n: Int) = "$label $n saniye içinde başlıyor"
    override val playNow = "Şimdi Oynat"
    override val skipIntro = "Girişi Atla"
    override val watchCredits = "Jeneriği izle"
    override val smoothBuffering = "Kesintisiz oynatma için arabelleğe alınıyor…"
    override val reconnecting = "Yeniden bağlanılıyor..."
    override val subtitleDelay = "Altyazı zamanlaması"
    override val subtitleDelayHint = "Sol: daha erken · Sağ: daha geç · OK: sıfırla"
    override fun seconds(value: String) = "$value sn"
    override val cinemaBadge = "Sinema kaydı"
    override fun msg(m: Msg) = when (m) {
        Msg.FindingSource -> "En iyi kaynak aranıyor..."
        Msg.FindingComplete -> "Tam sürüm aranıyor..."
        Msg.TryingAnother -> "Başka bir kaynak deneniyor..."
        Msg.SearchingIndexers -> "Dizinleyiciler taranıyor..."
        Msg.CheckingSources -> "Kaynaklar kontrol ediliyor..."
        Msg.NoSourceHint -> "Şu anda oynatılabilir bir kaynak bulunamadı. Biraz sonra tekrar dene."
        Msg.CouldNotRead -> "Video okunamadı"
        Msg.ConnectionLost -> "Bu torrent ile bağlantı koptu."
        Msg.PlaybackFailed -> "Oynatma başarısız oldu. Torrentte yeterli eş olmayabilir."
        Msg.Network -> "Nextflix sunucusuna ulaşılamadı."
        Msg.SwitchedHd -> "Daha akıcı oynatma için HD kaliteye geçildi"
        Msg.SwitchedFaster -> "Daha hızlı bir kaynağa geçildi"
        Msg.SwitchedHigher -> "Daha yüksek kaliteye geçildi"
        Msg.SwitchedTo4k -> "4K'ya geçildi"
        Msg.SwitchedSubtitles -> "Türkçe altyazılı sürüme geçildi"
        Msg.SubtitleFound -> "Türkçe altyazı bulundu"
        Msg.Connecting -> "Eşlere bağlanılıyor..."
        Msg.ReadingInfo -> "Video bilgileri okunuyor..."
        Msg.Buffering -> "Arabelleğe alınıyor..."
    }
}

internal val playerStrings = Localized<PlayerStrings>(en = PlayerEn, tr = PlayerTr)
