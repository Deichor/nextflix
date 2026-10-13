package com.nextflix.tv.core.nav

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

/**
 * A query from outside the app (system search, "search for X on Nextflix") waiting for the Search screen.
 * The app [offer]s it and opens Route.Search; the Search screen [take]s it.
 */
class PendingSearch internal constructor() {
    private val query = MutableStateFlow<String?>(null)

    val pending: StateFlow<String?> = query.asStateFlow()

    fun offer(text: String) {
        query.value = text.trim().take(MAX_LENGTH).ifEmpty { null }
    }

    /** The pending query, once. */
    fun take(): String? {
        val q = query.value ?: return null
        return if (query.compareAndSet(q, null)) q else null
    }

    companion object {
        const val MAX_LENGTH = 120
    }
}
