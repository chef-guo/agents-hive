import { Link, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useState, useRef, useEffect, useCallback } from 'react';
import { createPortal } from 'react-dom';
import { LogIn, PanelLeft } from 'lucide-react';
import { LanguageSwitcher } from './LanguageSwitcher';
import { ThemeToggle } from './ThemeToggle';
import { useHeaderStore } from '../../store/header';
import { useAuthStore } from '../../store/auth';

interface Props {
  connected: boolean;
  onToggleSidebar: () => void;
}

/** 全局顶栏：左侧折叠按钮 + 中间页面标题 + 右侧语言/主题/WS 状态 */
export function Header({ connected, onToggleSidebar }: Props) {
  const { t } = useTranslation();
  const location = useLocation();
  const { leftExtra, centerOverride, rightExtra } = useHeaderStore();
  const { user, authEnabled } = useAuthStore();
  const [menuOpen, setMenuOpen] = useState(false);
  const [menuAnchor, setMenuAnchor] = useState<DOMRect | null>(null);
  const menuTriggerRef = useRef<HTMLButtonElement>(null);
  const menuPanelRef = useRef<HTMLDivElement>(null);

  const updateMenuAnchor = useCallback(() => {
    const el = menuTriggerRef.current;
    if (!el) return;
    setMenuAnchor(el.getBoundingClientRect());
  }, []);

  const openUserMenu = () => {
    updateMenuAnchor();
    setMenuOpen(true);
  };

  const closeUserMenu = () => setMenuOpen(false);

  const handleLogout = () => {
    closeUserMenu();
    useAuthStore.getState().logout();
  };

  // 点击菜单外部时关闭（面板在 portal 中，需同时判断触发按钮）
  useEffect(() => {
    if (!menuOpen) return;
    const handler = (e: MouseEvent) => {
      const target = e.target as Node;
      if (menuTriggerRef.current?.contains(target)) return;
      if (menuPanelRef.current?.contains(target)) return;
      setMenuOpen(false);
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [menuOpen]);

  useEffect(() => {
    if (!menuOpen) return;
    const onLayout = () => updateMenuAnchor();
    window.addEventListener('resize', onLayout);
    window.addEventListener('scroll', onLayout, true);
    return () => {
      window.removeEventListener('resize', onLayout);
      window.removeEventListener('scroll', onLayout, true);
    };
  }, [menuOpen, updateMenuAnchor]);

  // 根据路由映射页面标题
  const pageTitleMap: Record<string, string> = {
    '/': t('chatLanding.title'),
    '/guide': t('nav.guide'),
    '/settings': t('nav.preferences'),
    '/admin': t('nav.adminDashboard'),
    '/admin/agents': t('nav.adminAgents'),
    '/admin/scheduled-tasks': t('nav.adminScheduledTasks'),
    '/admin/skills': t('nav.adminSkills'),
    '/admin/llm': t('nav.adminLLM'),
    '/admin/users': t('nav.adminUsers'),
    '/admin/usage': t('nav.adminUsage'),
    '/admin/auth-providers': t('nav.adminAuthProviders'),
    '/admin/prompts': t('nav.adminPrompts'),
    '/admin/quality-candidates': t('nav.adminQualityCandidates'),
    '/admin/quality-workbench': t('nav.adminQualityWorkbench'),
    '/admin/memory-governance': t('nav.adminMemoryGovernance'),
    '/admin/auto-optimization': t('nav.adminAutoOptimization'),
    '/admin/multi-agent': t('nav.adminMultiAgent'),
    '/admin/settings': t('nav.adminSettings'),
    '/admin/guide': t('nav.guide'),
  };

  const getTitle = () => {
    const path = location.pathname;
    if (path.startsWith('/sessions/')) return t('nav.sessions');
    if (path.startsWith('/admin/') && !pageTitleMap[path]) return t('nav.adminDashboard');
    return pageTitleMap[path] || '';
  };

  return (
    <header className="apple-header relative z-30 h-14 flex items-center justify-between px-4 border-b border-[var(--border-color)] shrink-0">
      {/* 左侧：侧栏折叠按钮 + 页面注入的额外内容 */}
      <div className="flex items-center gap-1 min-w-0">
        <button
          onClick={onToggleSidebar}
          className="p-2 rounded-lg text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-hover)] transition-colors shrink-0"
          title={t('nav.toggleSidebar')}
          aria-label={t('nav.toggleSidebar')}
        >
          <PanelLeft className="w-5 h-5" />
        </button>
        {leftExtra}
      </div>

      {/* 中间：页面标题或注入内容 */}
      <div className="absolute left-1/2 -translate-x-1/2 pointer-events-none">
        {centerOverride ?? (
          <h1 className="text-sm font-semibold text-[var(--text-primary)] whitespace-nowrap">
            {getTitle()}
          </h1>
        )}
      </div>

      {/* 右侧：提高 z-index，避免被中间 absolute 标题层叠遮挡 */}
      <div className="relative z-20 flex min-w-0 shrink-0 items-center gap-2">
        {rightExtra}

        {/* 用户信息（已登录） */}
        {user && (
          <div className="relative">
            <button
              ref={menuTriggerRef}
              type="button"
              className="flex items-center gap-2 px-2 py-1 rounded-[10px] hover:bg-[var(--bg-hover)] transition-colors"
              aria-haspopup="menu"
              aria-expanded={menuOpen}
              onClick={() => (menuOpen ? closeUserMenu() : openUserMenu())}
              onKeyDown={(e) => { if (e.key === 'Escape') closeUserMenu(); }}
            >
              {user.avatar_url ? (
                <img
                  src={user.avatar_url}
                  alt={user.display_name}
                  className="w-8 h-8 rounded-full object-cover"
                  onError={(e) => { (e.target as HTMLImageElement).style.display = 'none'; }}
                />
              ) : (
                <div className="w-8 h-8 rounded-full bg-[var(--accent)] flex items-center justify-center text-white text-xs font-medium">
                  {user.display_name?.charAt(0) || '?'}
                </div>
              )}
              <span className="text-sm text-[var(--text-primary)] max-w-[100px] truncate hidden sm:inline">
                {user.display_name}
              </span>
            </button>

            {menuOpen && menuAnchor && createPortal(
              <div
                ref={menuPanelRef}
                role="menu"
                className="user-menu-panel fixed z-[9999] w-48 py-1 rounded-[10px] border border-[var(--border-color)] bg-[var(--bg-primary)] shadow-xl"
                style={{
                  top: menuAnchor.bottom + 4,
                  right: window.innerWidth - menuAnchor.right,
                }}
                onKeyDown={(e) => { if (e.key === 'Escape') closeUserMenu(); }}
              >
                <div className="px-3 py-2 border-b border-[var(--border-color)]">
                  <p className="text-sm font-medium text-[var(--text-primary)] truncate">{user.display_name}</p>
                  <p className="text-xs text-[var(--text-secondary)] truncate">{user.email}</p>
                </div>
                {user.role === 'admin' && (
                  <Link
                    to="/admin"
                    role="menuitem"
                    className="block px-3 py-2 text-sm text-[var(--text-primary)] hover:bg-[var(--bg-hover)]"
                    onClick={closeUserMenu}
                  >
                    管理后台
                  </Link>
                )}
                <button
                  type="button"
                  role="menuitem"
                  onClick={handleLogout}
                  className="w-full text-left px-3 py-2 text-sm text-red-500 hover:bg-[var(--bg-hover)]"
                >
                  退出登录
                </button>
              </div>,
              document.body,
            )}
          </div>
        )}

        <LanguageSwitcher />
        <ThemeToggle />
        <div className="flex items-center gap-1.5 ml-1 px-2 py-1 rounded-lg text-xs text-[var(--text-secondary)]">
          <span className="relative flex h-2 w-2">
            {connected && (
              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
            )}
            <span className={`relative inline-flex rounded-full h-2 w-2 ${
              connected ? 'bg-emerald-500' : 'bg-red-500'
            }`} />
          </span>
          <span className="hidden sm:inline">
            {connected ? t('common.connected') : t('common.disconnected')}
          </span>
        </div>

        {!user && (
          <Link
            to="/login"
            className="inline-flex shrink-0 items-center gap-1.5 rounded-lg border border-[var(--border-color)] bg-[var(--bg-card)] px-2.5 py-1.5 text-sm font-medium text-[var(--text-primary)] shadow-sm hover:bg-[var(--bg-hover)] transition-colors"
            title={authEnabled === false ? t('nav.loginRequiresAuthHint') : undefined}
          >
            <LogIn className="h-4 w-4 text-[var(--accent-600)]" aria-hidden />
            <span>{t('nav.login')}</span>
          </Link>
        )}
      </div>
    </header>
  );
}
