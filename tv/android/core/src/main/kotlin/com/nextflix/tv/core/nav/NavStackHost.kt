package com.nextflix.tv.core.nav

import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.key
import androidx.compose.runtime.saveable.rememberSaveableStateHolder
import androidx.lifecycle.viewmodel.compose.LocalViewModelStoreOwner

/**
 * Composes the top entry of [stack] with its own ViewModelStore (viewModel { ... } is scoped to the screen)
 * and saved state (rememberSaveable survives while the screen is below others on the stack).
 */
@Composable
fun NavStackHost(stack: BackStack, content: @Composable (Route) -> Unit) {
    val holder = rememberSaveableStateHolder()
    val top = stack.top
    key(top.id) {
        holder.SaveableStateProvider(top.id) {
            CompositionLocalProvider(
                LocalViewModelStoreOwner provides top,
                LocalNavEntry provides top,
            ) {
                content(top.route)
            }
        }
    }
    LaunchedEffect(top.id) {
        stack.disposeRemoved { holder.removeState(it) }
    }
    // The player saves progress and releases its torrents when its ViewModel is cleared
    DisposableEffect(stack) { onDispose { stack.disposeAll() } }
}
