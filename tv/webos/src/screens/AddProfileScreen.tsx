import { useRef, useState } from 'react';
import { Check } from 'lucide-react';
import Avatar, { AVATAR_KEYS, PROFILE_COLORS } from '../components/Avatar';
import Keyboard from '../components/Keyboard';
import { createProfile } from '../lib/catalog';
import { useI18n, type Locale } from '../lib/i18n';
import { MAX_PROFILE_NAME } from '../lib/profile';
import { useFocusRestore, useRouter } from '../nav/router';

const LANGUAGES: Locale[] = ['en', 'tr'];

// Netflix "Add Profile": name on the on-screen keyboard, avatar, color and language
export default function AddProfileScreen({ count }: { count: number }) {
  const { t, locale } = useI18n();
  const router = useRouter();
  const [name, setName] = useState('');
  const [avatar, setAvatar] = useState(AVATAR_KEYS[count % AVATAR_KEYS.length]);
  const [color, setColor] = useState(PROFILE_COLORS[count % PROFILE_COLORS.length]);
  const [language, setLanguage] = useState<Locale>(locale);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const savingRef = useRef(false);
  useFocusRestore(true, 'kbd-a');

  const trimmed = name.trim();

  const save = async () => {
    if (!trimmed || savingRef.current) return;
    savingRef.current = true;
    setSaving(true);
    setError('');
    try {
      const p = await createProfile({ name: trimmed.slice(0, MAX_PROFILE_NAME), avatar, color, language });
      // The profiles screen below focuses the new tile when it comes back
      const below = router.stack[router.stack.length - 2];
      if (below) below.focusId = `profile-${p.id}`;
      if (!router.back()) router.reset({ name: 'profiles' });
    } catch (e) {
      setError(e instanceof Error && e.message ? e.message : t('profiles.couldNotSave'));
      savingRef.current = false;
      setSaving(false);
    }
  };

  return (
    <div className="screen add-profile-screen">
      <div className="brand brand-corner">NEXTFLIX</div>
      <h1 className="add-profile-title">{t('profiles.addProfile')}</h1>
      <p className="add-profile-hint">{t('profiles.addProfileHint')}</p>
      <div className="add-profile-body">
        <div className="add-profile-left">
          <div className="add-profile-name">
            <Avatar profile={{ avatar, color, name: trimmed }} className="add-profile-preview" />
            <div className="add-profile-field">
              <div className="text-field">
                {name ? name : <span className="text-placeholder">{t('profiles.name')}</span>}
                <span className="caret" />
              </div>
              <div className="add-profile-count">{name.length}/{MAX_PROFILE_NAME}</div>
            </div>
          </div>
          <Keyboard
            layout="text"
            value={name}
            max={MAX_PROFILE_NAME}
            onChange={v => { setName(v); setError(''); }}
          />
        </div>
        <div className="add-profile-right">
          <div className="field-label">{t('profiles.avatar')}</div>
          <div className="avatar-picker" data-nav-group="add-avatars">
            {AVATAR_KEYS.map(key => (
              <button
                key={key}
                type="button"
                className={`avatar-option ${key === avatar ? 'is-active' : ''}`}
                data-nav
                data-nav-id={`avatar-${key}`}
                data-nav-default={key === avatar ? '' : undefined}
                onClick={() => setAvatar(key)}
              >
                <Avatar profile={{ avatar: key, color, name: '' }} className="avatar-option-img" />
              </button>
            ))}
          </div>
          <div className="field-label">{t('profiles.color')}</div>
          <div className="color-picker" data-nav-group="add-colors">
            {PROFILE_COLORS.map(c => (
              <button
                key={c}
                type="button"
                className={`color-option ${c === color ? 'is-active' : ''}`}
                style={{ backgroundColor: c }}
                data-nav
                data-nav-id={`color-${c.slice(1)}`}
                data-nav-default={c === color ? '' : undefined}
                onClick={() => setColor(c)}
              >
                {c === color && <Check className="color-check" strokeWidth={3} />}
              </button>
            ))}
          </div>
          <div className="field-label">{t('profiles.language')}</div>
          <div className="add-profile-langs" data-nav-group="add-langs">
            {LANGUAGES.map(l => (
              <button
                key={l}
                type="button"
                className={`chip ${l === language ? 'is-active' : ''}`}
                data-nav
                data-nav-id={`lang-${l}`}
                data-nav-default={l === language ? '' : undefined}
                onClick={() => setLanguage(l)}
              >
                {t(l === 'en' ? 'language.en' : 'language.tr')}
              </button>
            ))}
          </div>
          <div className="add-profile-buttons" data-nav-group="add-actions">
            <button type="button" className="btn btn-primary" data-nav data-nav-id="add-save" disabled={!trimmed} onClick={save}>
              {saving ? t('common.saving') : t('common.save')}
            </button>
            <button type="button" className="btn btn-secondary" data-nav data-nav-id="add-cancel" onClick={() => router.back()}>
              {t('common.cancel')}
            </button>
          </div>
          <div className="add-profile-error">{error}</div>
        </div>
      </div>
    </div>
  );
}
