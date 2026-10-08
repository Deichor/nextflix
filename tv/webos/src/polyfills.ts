// Runtime APIs missing on older webOS engines (webOS 4: Chromium 53, webOS 5: Chromium 68).
// Syntax is lowered by the build (target chrome53); only APIs the app uses are listed.
import 'core-js/actual/object/entries';
import 'core-js/actual/object/values';
import 'core-js/actual/object/from-entries';
import 'core-js/actual/string/pad-start';
import 'core-js/actual/string/pad-end';
import 'core-js/actual/promise/finally';
import 'core-js/actual/array/flat';
import 'core-js/actual/array/includes';
import 'core-js/actual/global-this';
import 'abortcontroller-polyfill/dist/polyfill-patch-fetch';
