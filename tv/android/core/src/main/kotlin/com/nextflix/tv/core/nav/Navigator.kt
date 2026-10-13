package com.nextflix.tv.core.nav

import androidx.compose.runtime.Stable
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.focus.FocusRequester
import androidx.lifecycle.ViewModelStore
import androidx.lifecycle.ViewModelStoreOwner
import java.util.UUID

/**
 * Screen stack (webOS nav/router.tsx). Screens get it as a parameter. Only the top screen is composed;
 * the screens below keep their ViewModels and rememberSaveable state, and Back restores the focused element
 * (see core.ui.focus.restorableFocus / FocusRestore).
 */
@Stable
interface Navigator {
    /** The top screen. */
    val current: Route

    /** Opens [route] on top (Detail, Player, Setup from Settings, ...). */
    fun push(route: Route)

    /** Replaces the top screen (e.g. the player moving to the next episode, if it wants a new screen). */
    fun replace(route: Route)

    /** Clears the stack and opens [route] (menu tabs, picking a profile, saving the server). */
    fun reset(route: Route)

    /** Pops the top screen; false when it is the only one (the app then handles Back: menu, exit dialog). */
    fun back(): Boolean

    companion object {
        /** No-op navigator for @Preview. */
        val Preview: Navigator = object : Navigator {
            override val current: Route = Route.Home
            override fun push(route: Route) = Unit
            override fun replace(route: Route) = Unit
            override fun reset(route: Route) = Unit
            override fun back() = false
        }
    }
}

/** One screen on the stack: its route, ViewModels and the element focused when it was left. */
@Stable
class NavEntry internal constructor(val route: Route) : ViewModelStoreOwner {
    val id: String = UUID.randomUUID().toString()
    override val viewModelStore = ViewModelStore()

    /** Key of the last focused restorable element (restorableFocus). */
    var focusedKey: String? = null
        internal set

    internal val requesters = HashMap<String, FocusRequester>()
}

/** The entry being composed; null outside a NavStackHost (previews). */
val LocalNavEntry = staticCompositionLocalOf<NavEntry?> { null }

/** The [Navigator] implementation used by the app. */
class BackStack(initial: Route) : Navigator {
    private val entries = mutableStateListOf(NavEntry(initial))
    internal val removed = ArrayList<NavEntry>()

    val top: NavEntry get() = entries.last()
    val size: Int get() = entries.size
    override val current: Route get() = top.route

    override fun push(route: Route) {
        entries.add(NavEntry(route))
    }

    override fun replace(route: Route) {
        removed += entries.removeAt(entries.lastIndex)
        entries.add(NavEntry(route))
    }

    override fun reset(route: Route) {
        removed += entries.toList()
        entries.clear()
        entries.add(NavEntry(route))
    }

    override fun back(): Boolean {
        if (entries.size <= 1) return false
        removed += entries.removeAt(entries.lastIndex)
        return true
    }

    /** The host left for good (Activity destroyed): the stack lived in its composition, so every screen goes. */
    internal fun disposeAll() {
        removed += entries
        removed.forEach { it.viewModelStore.clear() }
        removed.clear()
    }

    /** Clears ViewModels and saved state of popped entries once they left the composition. */
    internal fun disposeRemoved(removeState: (String) -> Unit) {
        removed.forEach {
            it.viewModelStore.clear()
            removeState(it.id)
        }
        removed.clear()
    }
}
