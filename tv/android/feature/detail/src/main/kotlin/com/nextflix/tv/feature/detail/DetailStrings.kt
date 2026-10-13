package com.nextflix.tv.feature.detail

import com.nextflix.tv.core.i18n.Localized

internal interface DetailStrings {
    val episodes: String
    fun season(n: Int): String
    val moreLikeThis: String
    val cast: String
    val genres: String
    val director: String
    val createdBy: String
    val noDescription: String
    val notFound: String
    val loadFailed: String

    val favorite: String
    val unfavorite: String
    val love: String
    val like: String
    val dislike: String

    val cinemaWatchAnyway: String
    val cinemaConfirm: String
    val cinemaPlay: String
}

internal object DetailEn : DetailStrings {
    override val episodes = "Episodes"
    override fun season(n: Int) = "Season $n"
    override val moreLikeThis = "More Like This"
    override val cast = "Cast"
    override val genres = "Genres"
    override val director = "Director"
    override val createdBy = "Created by"
    override val noDescription = "No description available."
    override val notFound = "Title not found"
    override val loadFailed = "The details of this title could not be loaded."
    override val favorite = "Add to Favorites"
    override val unfavorite = "Remove from Favorites"
    override val love = "Love this!"
    override val like = "I like this"
    override val dislike = "Not for me"
    override val cinemaWatchAnyway = "Watch the cinema recording anyway"
    override val cinemaConfirm = "Cinema recordings have low picture and sound quality. Press again to play."
    override val cinemaPlay = "Play it"
}

internal object DetailTr : DetailStrings {
    override val episodes = "Bölümler"
    override fun season(n: Int) = "$n. Sezon"
    override val moreLikeThis = "Benzerleri"
    override val cast = "Oyuncular"
    override val genres = "Türler"
    override val director = "Yönetmen"
    override val createdBy = "Yaratıcı"
    override val noDescription = "Açıklama yok."
    override val notFound = "Yapım bulunamadı"
    override val loadFailed = "Bu yapımın ayrıntıları yüklenemedi."
    override val favorite = "Favorilere ekle"
    override val unfavorite = "Favorilerden çıkar"
    override val love = "Bayıldım"
    override val like = "Beğendim"
    override val dislike = "Bana göre değil"
    override val cinemaWatchAnyway = "Sinema kaydını yine de izle"
    override val cinemaConfirm = "Sinema kayıtlarının görüntü ve ses kalitesi düşüktür. Oynatmak için tekrar basın."
    override val cinemaPlay = "Yine de oynat"
}

internal val detailStrings = Localized<DetailStrings>(en = DetailEn, tr = DetailTr)
