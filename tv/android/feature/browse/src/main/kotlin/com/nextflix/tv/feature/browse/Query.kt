package com.nextflix.tv.feature.browse

import androidx.compose.runtime.Immutable
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/** One load of a rows screen (webOS lib/useQuery.ts): data, or failed. Neither: still loading. */
@Immutable
internal data class Query<out T>(val data: T? = null, val failed: Boolean = false) {
    val pending: Boolean get() = data == null && !failed

    /** A row's items: [map] of the data, [] after a failure (the row hides), null while loading (skeletons). */
    fun <R> items(map: (T) -> List<R>?): List<R>? = data?.let(map) ?: if (failed) emptyList() else null
}

/** [block]'s list, or [] when it fails (rows that hide on errors, like webOS `.catch(() => [])`). */
internal suspend fun <T> listOrEmpty(block: suspend () -> List<T>): List<T> =
    try {
        block()
    } catch (e: CancellationException) {
        throw e
    } catch (_: Exception) {
        emptyList()
    }

/**
 * A screen's loads. Data stay while they load again (focus and rows do not jump); a failed reload keeps the old
 * data. The repositories' answer cache makes a cached load complete without suspending, so a screen opened again
 * paints at once (viewModelScope runs on Main.immediate).
 */
internal abstract class LoadingViewModel : ViewModel() {
    private val jobs = HashMap<Any, Job>()

    protected fun <T> query(): MutableStateFlow<Query<T>> = MutableStateFlow(Query())

    protected fun <T> MutableStateFlow<Query<T>>.load(block: suspend () -> T) {
        jobs.remove(this)?.cancel()
        val target = this
        jobs[this] = viewModelScope.launch {
            try {
                val data = block()
                target.value = Query(data)
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) {
                target.update { if (it.data != null) it else Query(failed = true) }
            }
        }
    }
}
