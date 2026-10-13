package com.nextflix.tv

import androidx.activity.compose.BackHandler
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.CubicBezierEasing
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.ExperimentalComposeUiApi
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.foundation.focusGroup
import androidx.compose.ui.focus.focusRestorer
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.platform.LocalView
import com.nextflix.tv.core.Core
import com.nextflix.tv.core.LocalCore
import com.nextflix.tv.core.i18n.LocalAppLanguage
import com.nextflix.tv.core.i18n.strings
import com.nextflix.tv.core.model.Profile
import com.nextflix.tv.core.model.StreamProgress
import com.nextflix.tv.core.model.toPlayRequest
import com.nextflix.tv.core.nav.BackStack
import com.nextflix.tv.core.nav.NavStackHost
import com.nextflix.tv.core.nav.PlayRequest
import com.nextflix.tv.core.nav.Navigator
import com.nextflix.tv.core.nav.Route
import com.nextflix.tv.core.nav.tab
import com.nextflix.tv.core.ui.components.ConfirmDialog
import com.nextflix.tv.core.ui.components.LocalSubtitleSample
import com.nextflix.tv.core.ui.components.SideNav
import com.nextflix.tv.core.ui.focus.tryFocus
import com.nextflix.tv.core.ui.theme.NextflixTheme
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.feature.account.AddProfileScreen
import com.nextflix.tv.feature.account.IntroScreen
import com.nextflix.tv.feature.account.ProfilesScreen
import com.nextflix.tv.feature.account.SettingsScreen
import com.nextflix.tv.feature.account.SetupScreen
import com.nextflix.tv.feature.browse.HomeScreen
import com.nextflix.tv.feature.browse.MoviesScreen
import com.nextflix.tv.feature.browse.SurpriseScreen
import com.nextflix.tv.feature.browse.TvShowsScreen
import com.nextflix.tv.feature.detail.DetailScreen
import com.nextflix.tv.feature.player.PlayerScreen
import com.nextflix.tv.feature.search.SearchScreen
import com.nextflix.tv.feature.subtitles.SubtitleSampleRendererImpl
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.filterNotNull

/**
 * App root: waits for the stored session, then provides Core, the profile language and the theme, and shows the
 * screen stack (start: Setup without a server, else Profiles) with the intro over it on a cold start.
 */
@Composable
fun AppRoot(core: Core, coldStart: Boolean, links: MutableStateFlow<AppLink?>, onExit: () -> Unit) {
    val session by core.session.state.collectAsState()
    val state = session
    if (state == null) {
        Box(Modifier.fillMaxSize().background(NfColors.Background))
        return
    }
    CompositionLocalProvider(
        LocalCore provides core,
        LocalAppLanguage provides state.language,
        LocalSubtitleSample provides SubtitleSampleRendererImpl,
    ) {
        NextflixTheme {
            val stack = remember { BackStack(if (state.server == null) Route.Setup else Route.Profiles) }
            val zoom = remember { Animatable(1f) }
            var zooming by remember { mutableStateOf(false) }
            LaunchedEffect(zooming) {
                if (!zooming) {
                    zoom.snapTo(1f)
                    return@LaunchedEffect
                }
                // webOS .intro-on .app: 1.1 for 80 % of the intro, then eases to 1
                zoom.snapTo(1.1f)
                delay(INTRO_ZOOM_HOLD_MS)
                zoom.animateTo(1f, tween(INTRO_ZOOM_MS, easing = CubicBezierEasing(0.2f, 0.7f, 0.3f, 1f)))
            }
            Shell(stack, state.profile, onExit, Modifier.graphicsLayer { scaleX = zoom.value; scaleY = zoom.value })
            var intro by rememberSaveable { mutableStateOf(coldStart) }
            if (intro) IntroScreen(onDone = { intro = false }, onZoom = { zooming = it })

            LaunchedEffect(Unit) {
                links.filterNotNull().collect {
                    links.value = null
                    openLink(core, stack, it)
                }
            }
        }
    }
}

/**
 * The screen stack with the side menu around the tab screens, Back handling (webOS App.tsx) and the exit dialog.
 * Back: the previous screen; on a tab: Home; on Home: the menu, then "Exit?"; elsewhere at the root: "Exit?".
 * Screens that need Back themselves (closing a panel) register their own BackHandler, which runs first.
 */
@OptIn(ExperimentalComposeUiApi::class)
@Composable
private fun Shell(stack: BackStack, profile: Profile?, onExit: () -> Unit, modifier: Modifier = Modifier) {
    val route = stack.current
    val tab = route.tab
    val railFocus = remember { FocusRequester() }
    var railFocused by remember { mutableStateOf(false) }
    var exitAsk by remember { mutableStateOf(false) }

    BackHandler(enabled = !exitAsk) {
        if (stack.back()) return@BackHandler
        when {
            route == Route.Home -> if (railFocused || !railFocus.tryFocus()) exitAsk = true
            tab != null -> stack.reset(Route.Home)
            else -> exitAsk = true
        }
    }

    Box(modifier.fillMaxSize().background(NfColors.Background)) {
        Box(Modifier.fillMaxSize().focusRestorer().focusGroup()) {
            NavStackHost(stack) { r -> Screen(r, stack) }
        }
        if (tab != null) {
            SideNav(
                active = tab,
                profile = profile,
                onSelect = { stack.reset(it.route) },
                onProfile = { stack.reset(Route.Profiles) },
                activeRequester = railFocus,
                onFocusChange = { railFocused = it },
            )
        }
    }

    if (route is Route.Player) KeepScreenOn()

    if (exitAsk) {
        val s = strings()
        ConfirmDialog(
            title = s.exitTitle,
            confirm = s.exitConfirm,
            cancel = s.cancel,
            onConfirm = onExit,
            onCancel = { exitAsk = false },
        )
    }
}

/** Every route -> its feature entry point. */
@Composable
private fun Screen(route: Route, nav: Navigator) {
    when (route) {
        Route.Setup -> SetupScreen(nav)
        Route.Profiles -> ProfilesScreen(nav)
        is Route.AddProfile -> AddProfileScreen(route, nav)
        Route.Home -> HomeScreen(nav)
        Route.Movies -> MoviesScreen(nav)
        Route.TvShows -> TvShowsScreen(nav)
        Route.Search -> SearchScreen(nav)
        Route.Settings -> SettingsScreen(nav)
        Route.Surprise -> SurpriseScreen(nav)
        is Route.Detail -> DetailScreen(route, nav)
        is Route.Player -> PlayerScreen(route.request, nav)
    }
}

@Composable
private fun KeepScreenOn() {
    val view = LocalView.current
    DisposableEffect(view) {
        view.keepScreenOn = true
        onDispose { view.keepScreenOn = false }
    }
}

/**
 * Opens what an outside intent asked for. Search: the Search tab with the query (Profiles first when no profile
 * was picked yet). Watch Next card: switches to its profile, then Home > Detail > Player (resume or next episode).
 */
private suspend fun openLink(core: Core, stack: BackStack, link: AppLink) {
    if (core.session.server == null) return
    when (link) {
        is AppLink.Search -> {
            core.pendingSearch.offer(link.query)
            if (core.session.profile != null) stack.reset(Route.Search)
        }
        is AppLink.Open -> {
            val profile = core.session.profile?.takeIf { it.id == link.profileId }
                ?: orNull { core.profiles.list() }?.firstOrNull { it.id == link.profileId }
            if (profile == null) {
                stack.reset(Route.Profiles)
                return
            }
            core.session.setProfile(profile)
            val resume = if (link.play) orNull { resumeRequest(core, link) } else null
            stack.reset(Route.Home)
            stack.push(Route.Detail(link.type, link.tmdbId))
            if (resume != null) stack.push(Route.Player(resume))
        }
    }
}

/** The Continue Watching entry of the card (also the server's "next episode" entries), as a player request. */
private suspend fun resumeRequest(core: Core, link: AppLink.Open): PlayRequest? {
    fun StreamProgress.matches() = mediaType == link.type && tmdbId == link.tmdbId && season == link.season && episode == link.episode
    val entry = core.progress.continueWatching(link.profileId).firstOrNull { it.matches() }
        ?: core.progress.item(link.profileId, link.type, link.tmdbId, link.season, link.episode)
    return entry?.toPlayRequest()
}

private suspend fun <T> orNull(block: suspend () -> T): T? = try {
    block()
} catch (e: Exception) {
    if (e is CancellationException) throw e
    null
}

private const val INTRO_ZOOM_HOLD_MS = 2240L
private const val INTRO_ZOOM_MS = 560
