package com.nextflix.tv.feature.search

import com.nextflix.tv.core.i18n.Localized

internal interface SearchStrings {
    val placeholder: String
    val popular: String
    val results: String
    val browse: String
    val anime: String
    val noMatchesHint: String
    val noFilterMatches: String
    val noFilterMatchesHint: String
    val listening: String
    val voiceNoMatch: String
    val voiceUnavailable: String
    val voicePermission: String
    fun noMatches(query: String): String
    fun didYouMean(title: String): String
}

private object SearchEn : SearchStrings {
    override val placeholder = "Titles, people, genres"
    override val popular = "Popular Searches"
    override val results = "Results"
    override val browse = "Browse"
    override val anime = "Anime"
    override val noMatchesHint = "Try different keywords, or search for a movie or TV show title."
    override val noFilterMatches = "No titles match these filters."
    override val noFilterMatchesHint = "Try removing a genre or picking another type."
    override val listening = "Listening..."
    override val voiceNoMatch = "Didn't catch that. Press the mic and try again."
    override val voiceUnavailable = "Voice search isn't available on this TV."
    override val voicePermission = "Allow microphone access to search by voice."
    override fun noMatches(query: String) = "Your search for \"$query\" did not have any matches."
    override fun didYouMean(title: String) = "Did you mean $title?"
}

private object SearchTr : SearchStrings {
    override val placeholder = "Yapımlar, kişiler, türler"
    override val popular = "Popüler Aramalar"
    override val results = "Sonuçlar"
    override val browse = "Keşfet"
    override val anime = "Anime"
    override val noMatchesHint = "Farklı anahtar kelimeler dene ya da bir film veya dizi adı ara."
    override val noFilterMatches = "Bu filtrelere uyan bir yapım yok."
    override val noFilterMatchesHint = "Bir türü kaldırmayı ya da başka bir içerik tipi seçmeyi dene."
    override val listening = "Dinliyorum..."
    override val voiceNoMatch = "Anlaşılamadı. Mikrofona basıp tekrar dene."
    override val voiceUnavailable = "Sesli arama bu TV'de kullanılamıyor."
    override val voicePermission = "Sesle aramak için mikrofon iznini ver."
    override fun noMatches(query: String) = "\"$query\" araman hiçbir sonuçla eşleşmedi."
    override fun didYouMean(title: String) = "Bunu mu demek istediniz: $title?"
}

internal val searchStrings = Localized<SearchStrings>(en = SearchEn, tr = SearchTr)
