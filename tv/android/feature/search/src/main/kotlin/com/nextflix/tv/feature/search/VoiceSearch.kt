package com.nextflix.tv.feature.search

import android.Manifest
import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Bundle
import android.speech.RecognitionListener
import android.speech.RecognizerIntent
import android.speech.SpeechRecognizer
import androidx.activity.compose.ManagedActivityResultLauncher
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.ActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalContext
import androidx.core.content.ContextCompat
import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.i18n.LocalAppLanguage

/**
 * Voice search in the profile language (tr-TR / en-US). Prefers the in-app SpeechRecognizer (partial text shows in
 * the search field); without a recognition service, without the microphone permission or when the service fails,
 * it opens the system recognizer (RecognizerIntent), which needs no permission. [onResult] gets the final text.
 */
@Stable
internal class VoiceSearch(private val context: Context) {
    var listening by mutableStateOf(false)
        private set
    /** Text heard so far while [listening] */
    var partial by mutableStateOf("")
        private set
    /** Short note under the field (nothing heard, not available); cleared by the next start or typing */
    var message by mutableStateOf<String?>(null)

    internal var language: AppLanguage = AppLanguage.En
    internal var strings: SearchStrings? = null
    internal var onResult: (String) -> Unit = {}
    internal lateinit var permissionLauncher: ManagedActivityResultLauncher<String, Boolean>
    internal lateinit var intentLauncher: ManagedActivityResultLauncher<Intent, ActivityResult>

    private var recognizer: SpeechRecognizer? = null
    private var cancelled = false

    private val languageTag: String get() = if (language == AppLanguage.Tr) "tr-TR" else "en-US"

    /** Mic key: starts listening, or ends it early (the text heard so far is searched). */
    fun toggle() {
        if (listening) recognizer?.stopListening() else start()
    }

    fun start() {
        message = null
        if (!SpeechRecognizer.isRecognitionAvailable(context)) {
            startSystem()
            return
        }
        if (ContextCompat.checkSelfPermission(context, Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED) {
            startInApp()
        } else {
            permissionLauncher.launch(Manifest.permission.RECORD_AUDIO)
        }
    }

    /** Back while listening: stop without searching. */
    fun cancel() {
        if (!listening) return
        cancelled = true
        recognizer?.cancel()
        listening = false
        partial = ""
    }

    internal fun onPermission(granted: Boolean) {
        if (granted) startInApp() else startSystem(unavailable = strings?.voicePermission)
    }

    internal fun onSystemResult(result: ActivityResult) {
        if (result.resultCode != Activity.RESULT_OK) return
        val text = result.data?.getStringArrayListExtra(RecognizerIntent.EXTRA_RESULTS)?.firstOrNull()?.trim().orEmpty()
        if (text.isNotEmpty()) onResult(text) else message = strings?.voiceNoMatch
    }

    private fun recognizeIntent(action: String) = Intent(action)
        .putExtra(RecognizerIntent.EXTRA_LANGUAGE_MODEL, RecognizerIntent.LANGUAGE_MODEL_FREE_FORM)
        .putExtra(RecognizerIntent.EXTRA_LANGUAGE, languageTag)
        .putExtra(RecognizerIntent.EXTRA_LANGUAGE_PREFERENCE, languageTag)
        .putExtra(RecognizerIntent.EXTRA_MAX_RESULTS, 1)

    private fun startInApp() {
        val r = recognizer ?: try {
            SpeechRecognizer.createSpeechRecognizer(context).also {
                it.setRecognitionListener(listener)
                recognizer = it
            }
        } catch (_: RuntimeException) {
            null
        }
        if (r == null) {
            startSystem()
            return
        }
        cancelled = false
        partial = ""
        listening = true
        try {
            r.startListening(recognizeIntent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH).putExtra(RecognizerIntent.EXTRA_PARTIAL_RESULTS, true))
        } catch (_: RuntimeException) {
            listening = false
            startSystem()
        }
    }

    private fun startSystem(unavailable: String? = strings?.voiceUnavailable) {
        val intent = recognizeIntent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH)
            .putExtra(RecognizerIntent.EXTRA_PROMPT, strings?.listening)
        try {
            intentLauncher.launch(intent)
        } catch (_: ActivityNotFoundException) {
            message = unavailable
        }
    }

    private val listener = object : RecognitionListener {
        override fun onReadyForSpeech(params: Bundle?) = Unit
        override fun onBeginningOfSpeech() = Unit
        override fun onRmsChanged(rmsdB: Float) = Unit
        override fun onBufferReceived(buffer: ByteArray?) = Unit
        override fun onEndOfSpeech() = Unit
        override fun onEvent(eventType: Int, params: Bundle?) = Unit

        override fun onPartialResults(partialResults: Bundle?) {
            if (!listening) return
            first(partialResults)?.let { partial = it }
        }

        override fun onResults(results: Bundle?) {
            if (cancelled) return
            listening = false
            val text = first(results) ?: partial.trim()
            partial = ""
            if (text.isNotEmpty()) onResult(text) else message = strings?.voiceNoMatch
        }

        override fun onError(error: Int) {
            val wasListening = listening
            listening = false
            val heard = partial.trim()
            partial = ""
            if (cancelled || !wasListening) return
            when (error) {
                SpeechRecognizer.ERROR_NO_MATCH, SpeechRecognizer.ERROR_SPEECH_TIMEOUT ->
                    if (heard.isNotEmpty()) onResult(heard) else message = strings?.voiceNoMatch
                SpeechRecognizer.ERROR_INSUFFICIENT_PERMISSIONS -> startSystem(unavailable = strings?.voicePermission)
                // Network, busy, service gone, language missing: the system recognizer may still work
                else -> {
                    release()
                    startSystem()
                }
            }
        }
    }

    private fun first(bundle: Bundle?): String? =
        bundle?.getStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION)?.firstOrNull()?.trim()?.takeIf { it.isNotEmpty() }

    internal fun release() {
        recognizer?.destroy()
        recognizer = null
    }
}

@Composable
internal fun rememberVoiceSearch(onResult: (String) -> Unit): VoiceSearch {
    val context = LocalContext.current
    val voice = remember(context) { VoiceSearch(context) }
    val result by rememberUpdatedState(onResult)
    voice.language = LocalAppLanguage.current
    voice.strings = searchStrings.current()
    voice.onResult = { result(it) }
    voice.permissionLauncher = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission(), voice::onPermission)
    voice.intentLauncher = rememberLauncherForActivityResult(ActivityResultContracts.StartActivityForResult(), voice::onSystemResult)
    DisposableEffect(voice) {
        onDispose {
            voice.cancel()
            voice.release()
        }
    }
    return voice
}
