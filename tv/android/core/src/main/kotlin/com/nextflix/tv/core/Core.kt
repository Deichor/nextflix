package com.nextflix.tv.core

import android.content.Context
import androidx.compose.runtime.Composable
import androidx.compose.runtime.ReadOnlyComposable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.datastore.core.DataStore
import androidx.datastore.core.handlers.ReplaceFileCorruptionHandler
import androidx.datastore.preferences.core.PreferenceDataStoreFactory
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.datastore.preferences.preferencesDataStoreFile
import coil3.ImageLoader
import coil3.network.okhttp.OkHttpNetworkFetcherFactory
import coil3.request.crossfade
import com.nextflix.tv.core.data.CatalogRepository
import com.nextflix.tv.core.data.HomeSnapshotStore
import com.nextflix.tv.core.data.InstantRepository
import com.nextflix.tv.core.data.ProfileRepository
import com.nextflix.tv.core.data.ProgressRepository
import com.nextflix.tv.core.data.RatingsRepository
import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.nav.PendingSearch
import com.nextflix.tv.core.net.AnswerCache
import com.nextflix.tv.core.net.ApiClient
import com.nextflix.tv.core.net.ApiHeadersInterceptor
import com.nextflix.tv.core.net.CleartextGuard
import com.nextflix.tv.core.net.NextflixJson
import com.nextflix.tv.core.net.TmdbImages
import com.nextflix.tv.core.prefs.DevicePrefs
import com.nextflix.tv.core.prefs.PlayerPrefsStore
import com.nextflix.tv.core.session.Session
import com.nextflix.tv.core.stream.PlaybackCapsProvider
import com.nextflix.tv.core.stream.StreamWarmup
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.catch
import kotlinx.coroutines.launch
import okhttp3.Dispatcher
import okhttp3.OkHttpClient
import java.io.File
import java.io.IOException
import java.util.concurrent.TimeUnit

/**
 * The app's shared services, created once by the Application (see [CoreOwner]). Composables read it with
 * `LocalCore.current`; ViewModels get it in their factory: `viewModel { HomeViewModel(core) }`.
 */
class Core(
    context: Context,
    /** HTTP User-Agent, e.g. "Nextflix-AndroidTV/0.1.0" */
    userAgent: String,
    /** Creates the caps detection of :feature:player (the app hands it in; it may read devicePrefs). */
    playbackCapsFactory: (Core) -> PlaybackCapsProvider,
) {
    val appContext: Context = context.applicationContext

    /** Lives as long as the process: beacons, shared loads, prefs. */
    val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)

    // A damaged file starts over empty instead of failing every read (and the app with it)
    private val store: DataStore<Preferences> = ReadSafeStore(
        PreferenceDataStoreFactory.create(
            corruptionHandler = ReplaceFileCorruptionHandler { emptyPreferences() },
            scope = CoroutineScope(Dispatchers.IO + SupervisorJob()),
        ) { appContext.preferencesDataStoreFile("nextflix") },
    )

    val session = Session(store, scope, NextflixJson)
    val devicePrefs = DevicePrefs(store, scope)
    val playerPrefs = PlayerPrefsStore(store, scope, NextflixJson)

    /** What this TV plays without conversion (sent with streaming calls, shown in Settings). */
    val playbackCaps: PlaybackCapsProvider = playbackCapsFactory(this)

    /**
     * Shared OkHttp client: X-Lang / X-User-ID on /api calls, 8 s connect, 75 s read (resolve can take ~60 s).
     * The player may derive its own with `okHttp.newBuilder()` (same pool and dispatcher).
     */
    val okHttp: OkHttpClient = OkHttpClient.Builder()
        .dispatcher(Dispatcher().apply { maxRequestsPerHost = 12 })
        .connectTimeout(8, TimeUnit.SECONDS)
        .readTimeout(75, TimeUnit.SECONDS)
        .writeTimeout(15, TimeUnit.SECONDS)
        .addInterceptor(ApiHeadersInterceptor(session))
        .addNetworkInterceptor(CleartextGuard(session))
        .addNetworkInterceptor { chain ->
            chain.proceed(chain.request().newBuilder().header("User-Agent", userAgent).build())
        }
        .build()

    val api = ApiClient(session, okHttp, NextflixJson, scope)
    val cache = AnswerCache(session, scope)

    val profiles = ProfileRepository(api)
    val catalog = CatalogRepository(api, cache, session)
    val progress = ProgressRepository(api, cache)
    val ratings = RatingsRepository(api)
    val instant = InstantRepository(api, scope)
    val warmup = StreamWarmup(api, playbackCaps, scope)

    /** Home as last loaded (painted on a cold start before the refresh). */
    val homeSnapshots = HomeSnapshotStore(File(appContext.filesDir, "home"), NextflixJson, scope)

    /** Query from system search for the Search screen. */
    val pendingSearch = PendingSearch()

    /**
     * Starts Home's loads for the active profile (right after a pick), so Home finds them in flight or cached:
     * the same calls and cache keys as HomeViewModel.
     */
    fun prefetchHome() {
        val pid = session.profileId ?: return
        val pv = progress.version.value
        val rv = ratings.version.value
        background { catalog.home() }
        background { progress.continueWatchingCached(pid) }
        background { catalog.homeRecommendations(pid, pv, rv) }
        background { catalog.homeFavorites(pid, rv) }
    }

    /** Starts GET /api/home in [language] (Profiles screen, before a profile is picked). */
    fun prefetchHomeCatalog(language: AppLanguage) {
        background { catalog.home(language) }
    }

    private fun background(block: suspend () -> Unit) {
        scope.launch {
            try {
                block()
            } catch (e: Exception) {
                if (e is CancellationException) throw e
            }
        }
    }

    /** `<server>/tmdb-img/t/p/<size><path>`; "" without a path or server. Sizes: [TmdbImages]. */
    fun imageUrl(path: String?, size: String): String = TmdbImages.url(session.server, path, size)

    /**
     * Coil loader over the shared connection pool (images are not /api requests, so no profile headers). Its own
     * dispatcher, so API calls never queue behind card images.
     */
    fun newImageLoader(context: Context): ImageLoader {
        val images = okHttp.newBuilder()
            .dispatcher(Dispatcher().apply { maxRequestsPerHost = 6 })
            .readTimeout(20, TimeUnit.SECONDS)
            .build()
        return ImageLoader.Builder(context)
            .components { add(OkHttpNetworkFetcherFactory(callFactory = { images })) }
            .crossfade(false)
            .build()
    }
}

/** Implemented by the Application: `Context.core()` finds the graph from any context. */
interface CoreOwner {
    val core: Core
}

fun Context.core(): Core = (applicationContext as CoreOwner).core

val LocalCore = staticCompositionLocalOf<Core> { error("LocalCore is provided by the app") }

/** Shorthand for `LocalCore.current`. */
@Composable
@ReadOnlyComposable
fun core(): Core = LocalCore.current

/** Reads never fail: a file that cannot be read reads as empty (writes still report their errors). */
private class ReadSafeStore(private val inner: DataStore<Preferences>) : DataStore<Preferences> by inner {
    override val data: Flow<Preferences> = inner.data.catch { if (it is IOException) emit(emptyPreferences()) else throw it }
}
