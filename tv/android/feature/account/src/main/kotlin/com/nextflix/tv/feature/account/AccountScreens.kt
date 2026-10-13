package com.nextflix.tv.feature.account

import androidx.compose.runtime.Composable
import com.nextflix.tv.core.nav.Navigator
import com.nextflix.tv.core.nav.Route
import com.nextflix.tv.feature.account.intro.IntroContent

// Public entry points of :feature:account. Signatures are fixed (the app calls them).

/**
 * Server address (webOS screens/SetupScreen.tsx): first run, Settings > Server, Profiles > Change server.
 * On the first run the usual addresses are probed and the first that answers is preselected. "Save and continue"
 * checks /api/health and /api/profiles, saves the address and opens Profiles; "Test connection" only checks.
 */
@Composable
fun SetupScreen(nav: Navigator) = SetupContent(nav)

/**
 * "Who's watching?" (webOS screens/ProfilesScreen.tsx), the start screen once a server is set. Picking a profile
 * makes it active (the UI language switches at once) and opens Home.
 */
@Composable
fun ProfilesScreen(nav: Navigator) = ProfilesContent(nav)

/** New profile (webOS screens/AddProfileScreen.tsx): name, avatar, color and language. */
@Composable
fun AddProfileScreen(route: Route.AddProfile, nav: Navigator) = AddProfileContent(route, nav)

/**
 * Settings tab (webOS screens/SettingsScreen.tsx): server, profile language, HDR, autoplay, opening intro,
 * subtitle appearance with a live preview, the server's indexer and TorBox settings, and device info.
 */
@Composable
fun SettingsScreen(nav: Navigator) = SettingsContent(nav)

/**
 * Opening intro on a cold start (webOS components/Intro.tsx + lib/intro.ts): the red N lands, ribbons fan out,
 * zoom through into the app (~2.8 s), with the synthesized two-hit sting. Follows DevicePrefs (animation / sound);
 * any key skips. Calls [onDone] exactly once. [onZoom] true: zoom the app from 1.1 to 1 over the intro's length
 * (webOS .intro-on .app); false: back to 1 at once.
 */
@Composable
fun IntroScreen(onDone: () -> Unit, onZoom: (Boolean) -> Unit = {}) = IntroContent(onDone, onZoom)
