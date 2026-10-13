package com.nextflix.tv.core.net

/**
 * TMDB images through the server's disk-cached proxy `<server>/tmdb-img/t/p/<size><file>` (never image.tmdb.org:
 * DNS for it is unreliable on the LAN). Sizes the server accepts: w92 w154 w185 w300 w342 w500 w780 w1280 original.
 */
object TmdbImages {
    const val W92 = "w92"
    const val W154 = "w154"
    const val W185 = "w185"
    const val W300 = "w300"
    /** posters in rows */
    const val W342 = "w342"
    /** backdrop cards */
    const val W500 = "w500"
    /** heroes */
    const val W780 = "w780"
    /** full-screen backdrops */
    const val W1280 = "w1280"
    const val ORIGINAL = "original"

    private val SIZED = Regex("/t/p/[^/]+(/[^/?#]+)$")

    /**
     * [path] may be a bare TMDB path ("/abc.jpg"), an image.tmdb.org URL or "/tmdb-img/t/p/<size>/abc.jpg";
     * all become `<server>/tmdb-img/t/p/<size>/abc.jpg`. Empty for no path or no server.
     */
    fun url(server: String?, path: String?, size: String): String {
        if (path.isNullOrEmpty() || server.isNullOrEmpty()) return ""
        val file = SIZED.find(path)?.groupValues?.get(1) ?: if (path.startsWith("/")) path else "/$path"
        return "$server/tmdb-img/t/p/$size$file"
    }
}
