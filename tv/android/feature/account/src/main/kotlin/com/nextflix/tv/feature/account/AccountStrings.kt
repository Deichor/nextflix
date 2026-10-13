package com.nextflix.tv.feature.account

import com.nextflix.tv.core.i18n.Localized

// Texts from tv/webos/src/lib/i18n.ts (setup, profiles, settings) and web/src/shared/i18n (server settings).
internal interface AccountStrings {
    val setupTitle: String
    val setupAddress: String
    val setupHint: String
    val setupLanHint: String
    val setupTest: String
    val setupSave: String
    val setupTesting: String
    fun setupOk(version: String): String
    fun setupFailed(error: String): String
    val errorTimeout: String
    val errorBadAddress: String
    val errorUnreachable: String
    val errorUnknown: String

    val whoIsWatching: String
    val addProfile: String
    val addProfileHint: String
    fun couldNotReach(error: String): String
    val changeServer: String
    val name: String
    val avatar: String
    val color: String
    val language: String
    val couldNotSaveProfile: String
    val languageEn: String
    val languageTr: String

    val settingsTitle: String
    val server: String
    val displayLanguage: String
    fun languageSaveFailed(error: String): String
    val hdr: String
    val hdrAuto: String
    val hdrOn: String
    val hdrOff: String
    val autoplayNext: String
    val introAnimation: String
    val introSound: String
    val device: String
    val caps: String
    val version: String

    val advanced: String
    val advancedIntro: String
    val indexerTitle: String
    val indexerSummary: String
    val indexerKeyHint: String
    val jackettUrl: String
    val apiKey: String
    val bitsearch: String
    val bitsearchHint: String
    val debridTitle: String
    val debridSummary: String
    val debridKeyHint: String
    val debridEnabled: String
    val debridEnabledHint: String
    val debridPrefer: String
    val debridPreferHint: String
    val debridAddUncached: String
    val debridAddUncachedHint: String
    fun debridPlan(plan: String, date: String): String
    fun debridPlanNoDate(plan: String): String
    fun debridPlanExpired(plan: String): String
    val statusConnected: String
    val statusNotSet: String
    val statusError: String
    val statusChecking: String
    val saved: String
    val couldNotSave: String
    val couldNotLoad: String
    val couldNotReachServer: String
    val testConnection: String
    val jackettConnected: String
    val keySaved: String
    val keyNotSet: String
    val keyNew: String
    val keyRemoved: String
    val remove: String
    val show: String
    val hide: String
    val done: String
    val storedPlaceholder: String
}

private object AccountEn : AccountStrings {
    override val setupTitle = "Connect to your Nextflix server"
    override val setupAddress = "Server address"
    override val setupHint = "The address of the computer that runs Nextflix, with the server port (8252)."
    override val setupLanHint = "The TV and that computer must be on the same network. http://nextflix.local:8252 works on most networks."
    override val setupTest = "Test connection"
    override val setupSave = "Save and continue"
    override val setupTesting = "Connecting..."
    override fun setupOk(version: String) = "Connected to Nextflix $version".trimEnd()
    override fun setupFailed(error: String) = "Could not reach the server: $error"
    override val errorTimeout = "timeout"
    override val errorBadAddress = "not a valid address"
    override val errorUnreachable = "connection failed"
    override val errorUnknown = "unexpected error"

    override val whoIsWatching = "Who's watching?"
    override val addProfile = "Add Profile"
    override val addProfileHint = "Add a profile for another person watching Nextflix."
    override fun couldNotReach(error: String) = "Could not reach the Nextflix server. $error".trimEnd()
    override val changeServer = "Change server"
    override val name = "Name"
    override val avatar = "Avatar"
    override val color = "Color"
    override val language = "Language"
    override val couldNotSaveProfile = "Could not save profile."
    override val languageEn = "English"
    override val languageTr = "Türkçe"

    override val settingsTitle = "Settings"
    override val server = "Server"
    override val displayLanguage = "Display language"
    override fun languageSaveFailed(error: String) = "Could not change the language. $error".trimEnd()
    override val hdr = "HDR video"
    override val hdrAuto = "Automatic"
    override val hdrOn = "On"
    override val hdrOff = "Off"
    override val autoplayNext = "Autoplay next episode"
    override val introAnimation = "Opening animation"
    override val introSound = "Opening sound"
    override val device = "This TV"
    override val caps = "Plays without conversion"
    override val version = "App version"

    override val advanced = "Advanced settings"
    override val advancedIntro = "Server settings shared by every profile. Keys are stored on the server and never shown again."
    override val indexerTitle = "Indexer (Jackett)"
    override val indexerSummary = "Finds the torrents behind the Play button."
    override val indexerKeyHint = "Shown on the Jackett dashboard."
    override val jackettUrl = "Jackett URL"
    override val apiKey = "API key"
    override val bitsearch = "Also search Bitsearch"
    override val bitsearchHint = "A DHT index that finds releases no Jackett indexer lists, such as single-file uploads of local titles."
    override val debridTitle = "Debrid (TorBox)"
    override val debridSummary = "Plays releases TorBox has cached at full speed instead of from the swarm."
    override val debridKeyHint = "Shown in TorBox under Settings > API."
    override val debridEnabled = "Use TorBox"
    override val debridEnabledHint = "When TorBox is down or the plan has expired, playback silently uses torrents."
    override val debridPrefer = "Prefer cached releases"
    override val debridPreferHint = "A release TorBox has cached ranks first within its resolution."
    override val debridAddUncached = "Add uncached releases to TorBox"
    override val debridAddUncachedHint =
        "The torrent picked for playback is also sent to TorBox, so it is cached next time. Uses your TorBox download slots."
    override fun debridPlan(plan: String, date: String) = "$plan plan, until $date"
    override fun debridPlanNoDate(plan: String) = "$plan plan"
    override fun debridPlanExpired(plan: String) = "$plan plan (expired)"
    override val statusConnected = "Connected"
    override val statusNotSet = "Not set"
    override val statusError = "Error"
    override val statusChecking = "Checking..."
    override val saved = "Settings saved."
    override val couldNotSave = "Could not save settings"
    override val couldNotLoad = "Could not load settings"
    override val couldNotReachServer = "Could not reach the server"
    override val testConnection = "Test connection"
    override val jackettConnected = "Connected to Jackett"
    override val keySaved = "Saved"
    override val keyNotSet = "Not set"
    override val keyNew = "New key entered"
    override val keyRemoved = "Will be removed"
    override val remove = "Remove"
    override val show = "Show"
    override val hide = "Hide"
    override val done = "Done"
    override val storedPlaceholder = "Saved. Type to replace it"
}

private object AccountTr : AccountStrings {
    override val setupTitle = "Nextflix sunucuna bağlan"
    override val setupAddress = "Sunucu adresi"
    override val setupHint = "Nextflix'in çalıştığı bilgisayarın adresi ve sunucu portu (8252)."
    override val setupLanHint = "TV ile o bilgisayar aynı ağda olmalı. Çoğu ağda http://nextflix.local:8252 çalışır."
    override val setupTest = "Bağlantıyı test et"
    override val setupSave = "Kaydet ve devam et"
    override val setupTesting = "Bağlanılıyor..."
    override fun setupOk(version: String) = if (version.isEmpty()) "Nextflix sunucusuna bağlanıldı" else "Nextflix $version sunucusuna bağlanıldı"
    override fun setupFailed(error: String) = "Sunucuya ulaşılamadı: $error"
    override val errorTimeout = "zaman aşımı"
    override val errorBadAddress = "geçerli bir adres değil"
    override val errorUnreachable = "bağlantı kurulamadı"
    override val errorUnknown = "beklenmeyen hata"

    override val whoIsWatching = "Kim izliyor?"
    override val addProfile = "Profil Ekle"
    override val addProfileHint = "Nextflix'i izleyen başka biri için profil ekle."
    override fun couldNotReach(error: String) = "Nextflix sunucusuna ulaşılamadı. $error".trimEnd()
    override val changeServer = "Sunucuyu değiştir"
    override val name = "Ad"
    override val avatar = "Avatar"
    override val color = "Renk"
    override val language = "Dil"
    override val couldNotSaveProfile = "Profil kaydedilemedi."
    override val languageEn = "English"
    override val languageTr = "Türkçe"

    override val settingsTitle = "Ayarlar"
    override val server = "Sunucu"
    override val displayLanguage = "Görüntüleme dili"
    override fun languageSaveFailed(error: String) = "Dil değiştirilemedi. $error".trimEnd()
    override val hdr = "HDR video"
    override val hdrAuto = "Otomatik"
    override val hdrOn = "Açık"
    override val hdrOff = "Kapalı"
    override val autoplayNext = "Sonraki bölümü otomatik oynat"
    override val introAnimation = "Açılış animasyonu"
    override val introSound = "Açılış sesi"
    override val device = "Bu TV"
    override val caps = "Dönüştürmeden oynatılanlar"
    override val version = "Uygulama sürümü"

    override val advanced = "Gelişmiş ayarlar"
    override val advancedIntro = "Tüm profillerin paylaştığı sunucu ayarları. Anahtarlar sunucuda saklanır ve bir daha gösterilmez."
    override val indexerTitle = "Dizin sağlayıcı (Jackett)"
    override val indexerSummary = "Oynat düğmesinin arkasındaki torrentleri bulur."
    override val indexerKeyHint = "Jackett panelinde yazar."
    override val jackettUrl = "Jackett URL"
    override val apiKey = "API anahtarı"
    override val bitsearch = "Bitsearch'te de ara"
    override val bitsearchHint = "Hiçbir Jackett dizininde olmayan sürümleri (ör. yerli yapımların tek dosyalık yüklemelerini) bulan bir DHT dizini."
    override val debridTitle = "Debrid (TorBox)"
    override val debridSummary = "TorBox'ın önbelleğindeki sürümleri torrent ağı yerine tam hızda oynatır."
    override val debridKeyHint = "TorBox'ta Ayarlar > API altında gösterilir."
    override val debridEnabled = "TorBox'ı kullan"
    override val debridEnabledHint = "TorBox çalışmıyorsa veya planın süresi dolduysa oynatma sessizce torrentlerle devam eder."
    override val debridPrefer = "Önbellekteki sürümleri tercih et"
    override val debridPreferHint = "TorBox'ın önbelleğindeki bir sürüm kendi çözünürlüğünde öne geçer."
    override val debridAddUncached = "Önbellekte olmayanları TorBox'a ekle"
    override val debridAddUncachedHint =
        "Oynatma için seçilen torrent TorBox'a da gönderilir, böylece bir dahaki sefere önbellekte olur. TorBox indirme haklarını kullanır."
    override fun debridPlan(plan: String, date: String) = "$plan planı, $date tarihine kadar"
    override fun debridPlanNoDate(plan: String) = "$plan planı"
    override fun debridPlanExpired(plan: String) = "$plan planı (süresi doldu)"
    override val statusConnected = "Bağlı"
    override val statusNotSet = "Ayarlanmadı"
    override val statusError = "Hata"
    override val statusChecking = "Kontrol ediliyor..."
    override val saved = "Ayarlar kaydedildi."
    override val couldNotSave = "Ayarlar kaydedilemedi"
    override val couldNotLoad = "Ayarlar yüklenemedi"
    override val couldNotReachServer = "Sunucuya ulaşılamadı"
    override val testConnection = "Bağlantıyı test et"
    override val jackettConnected = "Jackett'e bağlanıldı"
    override val keySaved = "Kayıtlı"
    override val keyNotSet = "Ayarlanmadı"
    override val keyNew = "Yeni anahtar girildi"
    override val keyRemoved = "Kaldırılacak"
    override val remove = "Kaldır"
    override val show = "Göster"
    override val hide = "Gizle"
    override val done = "Tamam"
    override val storedPlaceholder = "Kayıtlı. Değiştirmek için yazın"
}

internal val accountStrings = Localized<AccountStrings>(en = AccountEn, tr = AccountTr)
