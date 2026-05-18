import { NavLink } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { LayoutDashboard, Bot, Zap, Settings, BookOpen, ArrowLeft, Users, BarChart2, ShieldCheck, FileText, BrainCircuit, FlaskConical, GitBranch, DatabaseZap, Sparkles, Network, CalendarClock, LibraryBig, Ticket } from 'lucide-react';
import { HiveLogo, NavItem } from './Sidebar';
import { useWsStore } from '../store/ws';
import { useAppStore } from '../store/app';

/** 管理后台导航项 */
const ADMIN_NAV = [
  { path: '/admin', label: 'nav.adminDashboard', icon: LayoutDashboard },
  { path: '/admin/agents', label: 'nav.adminAgents', icon: Bot },
  { path: '/admin/scheduled-tasks', label: 'nav.adminScheduledTasks', icon: CalendarClock },
  { path: '/admin/skills', label: 'nav.adminSkills', icon: Zap },
  { path: '/admin/llm', label: 'nav.adminLLM', icon: BrainCircuit },
  { path: '/admin/users', label: 'nav.adminUsers', icon: Users },
  { path: '/admin/usage', label: 'nav.adminUsage', icon: BarChart2 },
  { path: '/admin/auth-providers', label: 'nav.adminAuthProviders', icon: ShieldCheck },
  { path: '/admin/invite-codes', label: 'nav.adminInviteCodes', icon: Ticket },
  { path: '/admin/prompts', label: 'nav.adminPrompts', icon: FileText },
  { path: '/admin/kb', label: 'nav.adminKnowledgeBase', icon: LibraryBig },
  { path: '/admin/quality-candidates', label: 'nav.adminQualityCandidates', icon: FlaskConical },
  { path: '/admin/quality-workbench', label: 'nav.adminQualityWorkbench', icon: GitBranch },
  { path: '/admin/memory-governance', label: 'nav.adminMemoryGovernance', icon: DatabaseZap },
  { path: '/admin/auto-optimization', label: 'nav.adminAutoOptimization', icon: Sparkles },
  { path: '/admin/multi-agent', label: 'nav.adminMultiAgent', icon: Network },
  { path: '/admin/settings', label: 'nav.adminSettings', icon: Settings },
  { path: '/admin/guide', label: 'nav.guide', icon: BookOpen },
];

export function AdminSidebar() {
  const { t } = useTranslation();
  const sidebarOpen = useAppStore((s) => s.sidebarOpen);
  const connected = useWsStore((s) => s.connected);

  return (
    <aside
      className={`apple-sidebar h-full flex flex-col border-r border-[var(--border-color)] transition-all duration-300 shrink-0 ${
        sidebarOpen
          ? 'w-60 md:relative fixed z-40 shadow-xl md:shadow-none translate-x-0'
          : 'w-16 max-md:-translate-x-full max-md:absolute'
      }`}
    >
      {/* 顶部：品牌 + Admin 标识 */}
      <div className={`px-3 py-3 border-b border-[var(--border-color)] ${sidebarOpen ? '' : 'px-2 flex flex-col items-center'}`}>
        {sidebarOpen ? (
          <div className="flex items-center gap-2">
            <HiveLogo className="w-7 h-7 shrink-0" />
            <h1 className="text-sm font-bold text-gradient leading-tight font-display">Hive</h1>
            <span className="ml-1 px-1.5 py-0.5 text-[10px] font-medium rounded bg-[var(--accent-100)] text-[var(--accent-700)] dark:bg-[var(--accent-light)] dark:text-[var(--accent-300)] uppercase tracking-wide">
              Admin
            </span>
          </div>
        ) : (
          <HiveLogo className="w-7 h-7" />
        )}
      </div>

      {/* 中间：管理导航 */}
      <nav className="flex-1 px-2 py-3 space-y-0.5 overflow-y-auto">
        {ADMIN_NAV.map((item) => (
          <NavItem key={item.path} item={item} sidebarOpen={sidebarOpen} t={t} />
        ))}
      </nav>

      {/* 底部：返回聊天 + 连接状态 */}
      <div className="border-t border-[var(--border-color)]">
        <div className="px-2 py-2">
          <NavLink
            to="/"
            className={`flex items-center gap-3 px-3 py-2 text-sm rounded-lg transition-colors text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-secondary)] ${
              sidebarOpen ? '' : 'justify-center px-0'
            }`}
            title={!sidebarOpen ? t('nav.backToChat', 'Back to Chat') : undefined}
            aria-label={t('nav.backToChat', 'Back to Chat')}
          >
            <ArrowLeft className="w-[18px] h-[18px] shrink-0" />
            {sidebarOpen && <span>{t('nav.backToChat', 'Back to Chat')}</span>}
          </NavLink>
        </div>

        {/* 连接状态 */}
        <div className={`px-4 py-2.5 border-t border-[var(--border-color)] text-xs text-[var(--text-secondary)] ${sidebarOpen ? '' : 'px-2 text-center'}`}>
          <div className="flex items-center gap-2 justify-center">
            <span className={`w-1.5 h-1.5 rounded-full ${connected ? 'bg-emerald-500' : 'bg-red-500'}`} />
            {sidebarOpen && (
              <span>{connected ? t('common.connected', 'Connected') : t('common.disconnected', 'Disconnected')}</span>
            )}
          </div>
        </div>
      </div>
    </aside>
  );
}
