import './polyfills';
import '@fontsource/inter/latin-400.css';
import '@fontsource/inter/latin-ext-400.css';
import '@fontsource/inter/latin-600.css';
import '@fontsource/inter/latin-ext-600.css';
import '@fontsource/inter/latin-800.css';
import '@fontsource/inter/latin-ext-800.css';
import './styles.css';
import { createRoot } from 'react-dom/client';
import App from './App';
import { initSpatial } from './nav/spatial';
import { initCaps } from './lib/stream';

initSpatial();
// The panel resolution (HDR "auto") comes from a quick Luna call; start without waiting for it
initCaps();
createRoot(document.getElementById('root')!).render(<App />);
