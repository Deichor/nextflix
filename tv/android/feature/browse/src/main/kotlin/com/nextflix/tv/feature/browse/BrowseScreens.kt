package com.nextflix.tv.feature.browse

import androidx.compose.runtime.Composable
import com.nextflix.tv.core.nav.Navigator

// Public entry points of :feature:browse. Signatures are fixed (the app's NavHost calls them).
//
// Tab screens (Home, Movies, TV Shows) are drawn full screen UNDER the app's side menu rail: content keeps the left
// padding Dimens.ScreenPadH, and Left from the first card moves focus out of the screen (the rail takes it).
// The app handles Back on tab screens (Home: rail, then exit dialog).

/**
 * Home (webOS screens/HomeScreen.tsx + components/RowsScreen.tsx): billboard, Continue Watching (plays at once),
 * server recommendation rows, trending, Top 10 TR, favorites, popular, coming soon, new episodes, genre rows and
 * "Surprise me" under the last row.
 */
@Composable
fun HomeScreen(nav: Navigator) {
    HomeRoute(nav)
}

/** Movies tab (webOS screens/MoviesScreen.tsx): hero + movie rows (Top 10, popular, genres ...). */
@Composable
fun MoviesScreen(nav: Navigator) {
    CatalogRoute(nav, CatalogViewModel.Kind.Movies)
}

/** TV Shows tab (webOS screens/TvShowsScreen.tsx): hero + show rows (Top 10, provider rows ...). */
@Composable
fun TvShowsScreen(nav: Navigator) {
    CatalogRoute(nav, CatalogViewModel.Kind.Tv)
}

/**
 * Surprise me (webOS screens/SurpriseScreen.tsx): one title per press with the reason, Play (Detail with
 * autoplay), Details and "Something else". No side menu; Back returns to Home with the Surprise button focused.
 */
@Composable
fun SurpriseScreen(nav: Navigator) {
    SurpriseRoute(nav)
}
