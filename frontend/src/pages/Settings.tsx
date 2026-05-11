import { useTranslation } from 'react-i18next';
import { useAppStore } from '../store/app';
import { Sun, Moon, Languages } from 'lucide-react';
import { WeChatConnectionPanel } from '../components/settings/WeChatConnectionPanel';

export function Settings() {
  const { t } = useTranslation();
  const theme = useAppStore((s) => s.theme);
  const language = useAppStore((s) => s.language);
  const setTheme = useAppStore((s) => s.setTheme);
  const setLanguage = useAppStore((s) => s.setLanguage);

  return (
    <div className="p-6 max-w-5xl mx-auto">
      <div className="mb-6">
        <h2 className="text-xl font-semibold text-[var(--text-primary)] font-display">
          {t('nav.preferences')}
        </h2>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">
          {t('settings.subtitle', '管理个人偏好和用户级连接。')}
        </p>
      </div>

      <div className="mb-6 bg-[var(--bg-card)] border border-[var(--border-color)] rounded-2xl shadow-sm overflow-hidden">
        {/* 主题 */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-[var(--border-color)]">
          <div className="flex items-center gap-3">
            {theme === 'dark' ? (
              <Moon className="w-4 h-4 text-[var(--text-secondary)]" />
            ) : (
              <Sun className="w-4 h-4 text-[var(--text-secondary)]" />
            )}
            <span className="text-sm text-[var(--text-primary)]">{t('header.switchTheme')}</span>
          </div>
          <div className="flex gap-1 bg-[var(--bg-secondary)] rounded-lg p-0.5">
            <button
              onClick={() => setTheme('light')}
              className={`px-3 py-1 text-sm rounded-md transition-colors ${
                theme === 'light'
                  ? 'bg-[var(--bg-card)] text-[var(--text-primary)] shadow-sm'
                  : 'text-[var(--text-secondary)] hover:text-[var(--text-primary)]'
              }`}
            >
              {t('settings.lightTheme', 'Light')}
            </button>
            <button
              onClick={() => setTheme('dark')}
              className={`px-3 py-1 text-sm rounded-md transition-colors ${
                theme === 'dark'
                  ? 'bg-[var(--bg-card)] text-[var(--text-primary)] shadow-sm'
                  : 'text-[var(--text-secondary)] hover:text-[var(--text-primary)]'
              }`}
            >
              {t('settings.darkTheme', 'Dark')}
            </button>
          </div>
        </div>

        {/* 语言 */}
        <div className="flex items-center justify-between px-5 py-4">
          <div className="flex items-center gap-3">
            <Languages className="w-4 h-4 text-[var(--text-secondary)]" />
            <span className="text-sm text-[var(--text-primary)]">{t('header.switchLanguage')}</span>
          </div>
          <div className="flex gap-1 bg-[var(--bg-secondary)] rounded-lg p-0.5">
            <button
              onClick={() => setLanguage('zh')}
              className={`px-3 py-1 text-sm rounded-md transition-colors ${
                language === 'zh'
                  ? 'bg-[var(--bg-card)] text-[var(--text-primary)] shadow-sm'
                  : 'text-[var(--text-secondary)] hover:text-[var(--text-primary)]'
              }`}
            >
              中文
            </button>
            <button
              onClick={() => setLanguage('en')}
              className={`px-3 py-1 text-sm rounded-md transition-colors ${
                language === 'en'
                  ? 'bg-[var(--bg-card)] text-[var(--text-primary)] shadow-sm'
                  : 'text-[var(--text-secondary)] hover:text-[var(--text-primary)]'
              }`}
            >
              English
            </button>
          </div>
        </div>
      </div>

      <WeChatConnectionPanel />
    </div>
  );
}
