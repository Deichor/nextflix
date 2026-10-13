package com.nextflix.tv

import android.app.Application
import coil3.ImageLoader
import coil3.PlatformContext
import coil3.SingletonImageLoader
import com.nextflix.tv.core.Core
import com.nextflix.tv.core.CoreOwner
import com.nextflix.tv.feature.player.AndroidPlaybackCaps
import kotlinx.coroutines.launch

class NextflixApp : Application(), CoreOwner, SingletonImageLoader.Factory {
    override val core: Core by lazy {
        Core(
            context = this,
            userAgent = "Nextflix-AndroidTV/${BuildConfig.VERSION_NAME}",
            playbackCapsFactory = { AndroidPlaybackCaps(this, it.devicePrefs) },
        )
    }

    val watchNext: WatchNext by lazy { WatchNext(this, core) }

    override fun onCreate() {
        super.onCreate()
        // Codec detection takes a moment on slow boxes: done once here, off the main thread
        core.scope.launch { core.playbackCaps.current() }
        watchNext.start()
    }

    override fun newImageLoader(context: PlatformContext): ImageLoader = core.newImageLoader(context)
}
