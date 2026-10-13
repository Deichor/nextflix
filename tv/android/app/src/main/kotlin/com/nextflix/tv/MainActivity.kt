package com.nextflix.tv

import android.content.Intent
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import com.nextflix.tv.core.core
import kotlinx.coroutines.flow.MutableStateFlow

class MainActivity : ComponentActivity() {
    /** The latest outside request (search, Watch Next card); AppRoot handles it and clears it. */
    private val links = MutableStateFlow<AppLink?>(null)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val core = applicationContext.core()
        // The intro plays on a cold start from the launcher only, not when the activity is recreated
        val coldStart = savedInstanceState == null
        if (coldStart) links.value = AppLink.of(intent, this)
        setContent {
            AppRoot(
                core = core,
                coldStart = coldStart && links.value == null,
                links = links,
                onExit = ::finish,
            )
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        AppLink.of(intent, this)?.let { links.value = it }
    }

    override fun onStop() {
        super.onStop()
        (application as NextflixApp).watchNext.syncNow()
    }
}
