package com.nextflix.tv

import android.app.SearchManager
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.MediaStore
import com.nextflix.tv.core.model.MediaType
import com.nextflix.tv.core.nav.PendingSearch
import java.security.SecureRandom

/** What an outside intent asks the app to show: a search, or a title from the home screen's Watch Next row. */
sealed interface AppLink {
    /** ACTION_SEARCH / MEDIA_PLAY_FROM_SEARCH ("search for X on Nextflix"). */
    data class Search(val query: String) : AppLink

    /** A Watch Next card: the title's Detail page, or the player when [play] (resume or the next episode). */
    data class Open(
        val profileId: Int,
        val type: MediaType,
        val tmdbId: Int,
        val season: Int = 0,
        val episode: Int = 0,
        val play: Boolean = false,
    ) : AppLink {
        /** Explicit intent for MainActivity (Watch Next intent URI). */
        fun intent(context: Context): Intent = Intent(context, MainActivity::class.java)
            .setAction(Intent.ACTION_VIEW)
            .setData(
                Uri.Builder().scheme(SCHEME).authority(OPEN)
                    .appendQueryParameter("profile", profileId.toString())
                    .appendQueryParameter("type", type.apiName)
                    .appendQueryParameter("id", tmdbId.toString())
                    .appendQueryParameter("season", season.toString())
                    .appendQueryParameter("episode", episode.toString())
                    .appendQueryParameter("play", if (play) "1" else "0")
                    .appendQueryParameter("token", LinkToken.get(context))
                    .build(),
            )
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
    }

    companion object {
        private const val SCHEME = "nextflix"
        private const val OPEN = "open"

        /** [context]: checks the per-install token of Watch Next links (other apps may send this intent too). */
        fun of(intent: Intent?, context: Context): AppLink? {
            intent ?: return null
            return when (intent.action) {
                Intent.ACTION_SEARCH, MediaStore.INTENT_ACTION_MEDIA_PLAY_FROM_SEARCH ->
                    intent.getStringExtra(SearchManager.QUERY)?.trim()?.take(PendingSearch.MAX_LENGTH)
                        ?.takeIf { it.isNotEmpty() }?.let(::Search)
                Intent.ACTION_VIEW -> open(intent.data)?.takeIf { intent.data?.getQueryParameter("token") == LinkToken.get(context) }
                else -> null
            }
        }

        private fun open(uri: Uri?): Open? {
            if (uri == null || uri.scheme != SCHEME || uri.authority != OPEN) return null
            fun int(name: String) = uri.getQueryParameter(name)?.toIntOrNull()
            val type = MediaType.of(uri.getQueryParameter("type")) ?: return null
            val id = int("id")?.takeIf { it > 0 } ?: return null
            val profile = int("profile") ?: return null
            return Open(
                profileId = profile,
                type = type,
                tmdbId = id,
                season = int("season")?.coerceAtLeast(0) ?: 0,
                episode = int("episode")?.coerceAtLeast(0) ?: 0,
                play = uri.getQueryParameter("play") == "1",
            )
        }
    }
}

/**
 * Random secret of this install carried by the Watch Next cards' links: MainActivity is exported (launcher), so any
 * app could otherwise switch the profile and start playback with a crafted nextflix://open link.
 */
private object LinkToken {
    @Volatile
    private var token: String? = null

    fun get(context: Context): String = token ?: synchronized(this) {
        token ?: run {
            val prefs = context.applicationContext.getSharedPreferences("links", Context.MODE_PRIVATE)
            prefs.getString(KEY, null) ?: ByteArray(16).also { SecureRandom().nextBytes(it) }
                .joinToString("") { "%02x".format(it) }
                .also { prefs.edit().putString(KEY, it).apply() }
        }.also { token = it }
    }

    private const val KEY = "token"
}
