import { useEffect, useState } from 'react';
import { NavLink, useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { BookOpen, Trash2, Plus, MessageSquare, Search, ExternalLink, Star, Tag, Play, Settings as SettingsIcon } from 'lucide-react';
import { useSessionStore } from '../store/session';
import { useNodeClient } from '../hooks/useNodeClient';
import { useAppStore } from '../store/app';
import { useWsStore } from '../store/ws';
import { useChatStore } from '../store/chat';
import { useAgentActivityStore } from '../store/agentActivity';
import { TagEditor } from '../components/session/TagEditor';
import type { Session } from '../types/api';

function SessionStatusDot({ sessionId }: { sessionId: string }) {
  const status = useAgentActivityStore((s) => s.sessionStatus[sessionId]);
  if (!status || status === 'idle') return null;
  return (
    <span
      className={`shrink-0 w-1.5 h-1.5 rounded-full ${
        status === 'running'
          ? 'bg-green-500 animate-pulse'
          : 'bg-red-500'
      }`}
    />
  );
}

/** 底部导航 */
const BOTTOM_NAV = [
  { path: '/replay', label: 'nav.replay', icon: Play },
  { path: '/guide', label: 'nav.guide', icon: BookOpen },
  { path: '/settings', label: 'nav.preferences', icon: SettingsIcon },
];

export function HiveLogo({ className = '' }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 48 48" fill="none" xmlns="http://www.w3.org/2000/svg">
      <defs>
        <linearGradient id="hex-grad" x1="4" y1="4" x2="44" y2="44" gradientUnits="userSpaceOnUse">
          <stop offset="0%" stopColor="#CFFAFE"/>
          <stop offset="25%" stopColor="#93C5FD"/>
          <stop offset="50%" stopColor="#60A5FA"/>
          <stop offset="76%" stopColor="#6366F1"/>
          <stop offset="100%" stopColor="#4338CA"/>
        </linearGradient>
      </defs>
      <polygon points="37,18 42.2,21 42.2,27 37,30 31.8,27 31.8,21" fill="url(#hex-grad)"/>
      <polygon points="11,18 16.2,21 16.2,27 11,30 5.8,27 5.8,21" fill="url(#hex-grad)"/>
      <polygon points="30.5,6.74 35.7,9.74 35.7,15.74 30.5,18.74 25.3,15.74 25.3,9.74" fill="url(#hex-grad)"/>
      <polygon points="17.5,6.74 22.7,9.74 22.7,15.74 17.5,18.74 12.3,15.74 12.3,9.74" fill="url(#hex-grad)"/>
      <polygon points="30.5,29.26 35.7,32.26 35.7,38.26 30.5,41.26 25.3,38.26 25.3,32.26" fill="url(#hex-grad)"/>
      <polygon points="17.5,29.26 22.7,32.26 22.7,38.26 17.5,41.26 12.3,38.26 12.3,32.26" fill="url(#hex-grad)"/>
    </svg>
  );
}

export function NavItem({ item, sidebarOpen, t }: { item: { path: string; label: string; icon: React.ComponentType<{ className?: string }> }; sidebarOpen: boolean; t: (key: string) => string }) {
  const Icon = item.icon;
  return (
    <NavLink
      to={item.path}
      end={item.path === '/'}
      className={({ isActive }) =>
        `flex items-center gap-3 px-3 py-2 text-sm rounded-lg transition-colors ${
          sidebarOpen ? '' : 'justify-center px-0'
        } ${
          isActive
            ? 'bg-[var(--bg-secondary)] text-[var(--text-primary)] font-medium'
            : 'text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-secondary)]'
        }`
      }
      title={!sidebarOpen ? t(item.label) : undefined}
      aria-label={t(item.label)}
    >
      <Icon className="w-[18px] h-[18px] shrink-0" />
      {sidebarOpen && <span>{t(item.label)}</span>}
    </NavLink>
  );
}

/** 单个会话行（展开态） */
function SessionRow({ s, currentSessionId, chatSessionId, isBusy }: {
  s: Session;
  currentSessionId?: string;
  chatSessionId: string | null;
  isBusy: boolean;
}) {
  const { t } = useTranslation();
  const client = useNodeClient();
  const navigate = useNavigate();
  const starSession = useSessionStore((st) => st.starSession);
  const updateSessionTags = useSessionStore((st) => st.updateSessionTags);
  const deleteSession = useSessionStore((st) => st.deleteSession);
  const [tagEditorOpen, setTagEditorOpen] = useState(false);

  return (
    <>
      <div className="group relative flex flex-col">
        <div className="flex items-center">
          <NavLink
            to={`/sessions/${s.id}`}
            className={({ isActive }) =>
              `flex-1 min-w-0 px-3 py-1.5 text-sm rounded-lg truncate transition-colors pr-16 ${
                isActive
                  ? 'text-[var(--text-primary)] bg-[var(--bg-secondary)] font-medium'
                  : 'text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-secondary)]'
              }`
            }
          >
            <div className="flex items-center gap-1.5 min-w-0">
              {s.is_starred && (
                <Star className="w-3 h-3 shrink-0 text-[var(--accent-500)] fill-[var(--accent-500)]" />
              )}
              <span className="truncate">
                {s.name || (s.message_count === 0 ? t('sessions.newSession', '新会话') : s.id.slice(0, 8))}
              </span>
              <SessionStatusDot sessionId={s.id} />
            </div>
          </NavLink>

          {/* 操作按钮组（hover 显示） */}
          <div className="absolute right-1 flex items-center gap-0.5 opacity-0 group-hover:opacity-100 transition-opacity">
            {/* 回放 */}
            <button
              onClick={(e) => { e.preventDefault(); navigate(`/sessions/${s.id}/replay`); }}
              className="p-1.5 rounded text-[var(--text-secondary)] hover:text-emerald-500 hover:bg-emerald-50 dark:hover:bg-emerald-900/20 transition-all"
              title={t('sessions.replay', '回放')}
            >
              <Play className="w-3 h-3" />
            </button>
            {/* 标签编辑 */}
            <button
              onClick={(e) => { e.preventDefault(); setTagEditorOpen(true); }}
              className="p-1.5 rounded text-[var(--text-secondary)] hover:text-[var(--accent-500)] hover:bg-[var(--accent-50)] dark:hover:bg-[var(--accent-light)] transition-all"
              title={t('tags.edit', '编辑标签')}
            >
              <Tag className="w-3 h-3" />
            </button>
            {/* 收藏 */}
            <button
              onClick={(e) => { e.preventDefault(); starSession(client, s.id, !s.is_starred); }}
              className={`p-1.5 rounded transition-all ${
                s.is_starred
                  ? 'text-[var(--accent-500)] hover:text-[var(--accent-600)] hover:bg-[var(--accent-50)] dark:hover:bg-[var(--accent-light)]'
                  : 'text-[var(--text-secondary)] hover:text-[var(--accent-500)] hover:bg-[var(--accent-50)] dark:hover:bg-[var(--accent-light)]'
              }`}
              title={s.is_starred ? t('sessions.unstar', '取消收藏') : t('sessions.star', '收藏')}
            >
              <Star className={`w-3 h-3 ${s.is_starred ? 'fill-[var(--accent-500)]' : ''}`} />
            </button>
            {/* 删除 */}
            <button
              onClick={async (e) => {
                e.preventDefault();
                if (!confirm(t('sessions.deleteConfirm', '确认删除该会话？'))) return;
                await deleteSession(client, s.id);
                if (currentSessionId === s.id) navigate('/');
              }}
              disabled={s.id === chatSessionId && isBusy}
              className="p-1.5 rounded text-[var(--text-secondary)] hover:text-red-500 hover:bg-red-50 dark:hover:bg-red-900/20 transition-all disabled:opacity-30 disabled:cursor-not-allowed disabled:hover:text-[var(--text-secondary)] disabled:hover:bg-transparent"
              title={s.id === chatSessionId && isBusy ? t('sessions.busyCannotDelete', 'AI 正在输出，无法删除') : t('sessions.delete', '删除会话')}
            >
              <Trash2 className="w-3 h-3" />
            </button>
          </div>
        </div>

        {/* 标签 chips */}
        {s.tags && s.tags.length > 0 && (
          <div className="flex flex-wrap gap-1 px-3 pb-1">
            {s.tags.slice(0, 3).map((tag) => (
              <span
                key={tag}
                className="inline-block px-1.5 py-0.5 rounded-full bg-[var(--bg-secondary)] text-[var(--text-secondary)] text-[10px] leading-tight"
              >
                {tag}
              </span>
            ))}
            {s.tags.length > 3 && (
              <span className="inline-block px-1.5 py-0.5 rounded-full bg-[var(--bg-secondary)] text-[var(--text-secondary)] text-[10px] leading-tight">
                +{s.tags.length - 3}
              </span>
            )}
          </div>
        )}
      </div>

      {tagEditorOpen && (
        <TagEditor
          sessionId={s.id}
          initialTags={s.tags || []}
          onSave={(tags) => updateSessionTags(client, s.id, tags)}
          onClose={() => setTagEditorOpen(false)}
        />
      )}
    </>
  );
}

export function Sidebar() {
  const { t } = useTranslation();
  const client = useNodeClient();
  const sessions = useSessionStore((s) => s.sessions);
  const fetchSessions = useSessionStore((s) => s.fetchSessions);
  const createSession = useSessionStore((s) => s.createSession);
  const sidebarOpen = useAppStore((s) => s.sidebarOpen);
  const connected = useWsStore((s) => s.connected);
  const chatSessionId = useChatStore((s) => s.currentSessionId);
  const isBusy = useChatStore((s) => s.sending || s.streaming);
  const navigate = useNavigate();
  const { id: currentSessionId } = useParams<{ id: string }>();
  const [creating, setCreating] = useState(false);
  const [displayCount, setDisplayCount] = useState(10);
  const [searchQuery, setSearchQuery] = useState('');
  const [tooltip, setTooltip] = useState<{ label: string; y: number } | null>(null);

  useEffect(() => {
    fetchSessions(client);
  }, [client, fetchSessions]);

  const handleNewSession = async () => {
    const emptySession = sessions.find(s => s.message_count === 0);
    if (emptySession) {
      navigate(`/sessions/${emptySession.id}`);
      return;
    }
    setCreating(true);
    try {
      const id = await createSession(client, t('sessions.newSession', '新会话'));
      navigate(`/sessions/${id}`);
    } catch {
      // 错误已在 store 中处理
    }
    setCreating(false);
  };

  const filteredSessions = searchQuery
    ? sessions.filter((s) => {
        const name = s.name || s.id;
        const tagMatch = s.tags?.some((tag) => tag.toLowerCase().includes(searchQuery.toLowerCase()));
        return name.toLowerCase().includes(searchQuery.toLowerCase()) || tagMatch;
      })
    : sessions;

  return (
    <aside
      className={`apple-sidebar h-full flex flex-col border-r border-[var(--border-color)] transition-all duration-300 shrink-0 ${
        sidebarOpen
          ? 'w-60 md:relative fixed z-40 shadow-xl md:shadow-none translate-x-0'
          : 'w-16 max-md:-translate-x-full max-md:absolute'
      }`}
    >
      {/* 顶部：品牌 */}
      <div className={`h-14 px-3 border-b border-[var(--border-color)] flex items-center shrink-0 ${sidebarOpen ? '' : 'px-2 justify-center'}`}>
        <div className="flex items-center gap-2">
          <HiveLogo className="w-7 h-7 shrink-0" />
          {sidebarOpen && <h1 className="text-sm font-bold text-gradient leading-tight font-display">Hive</h1>}
        </div>
      </div>

      {/* 会话列表 */}
      {sidebarOpen && (
        <div className="flex-1 overflow-y-auto px-2 py-2">
          {/* 搜索栏 + 新建按钮 */}
          <div className="flex items-center gap-1.5 px-1 mb-2">
            <div className="relative flex-1">
              <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-[var(--text-secondary)]" />
              <input
                type="text"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder={t('sidebar.searchSessions', '搜索会话...')}
                className="w-full pl-8 pr-2 py-1.5 text-xs rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] placeholder:text-[var(--text-secondary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)] focus:border-[var(--accent)]"
              />
            </div>
            <button
              onClick={handleNewSession}
              disabled={creating}
              className="p-1.5 rounded-lg text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-secondary)] transition-colors disabled:opacity-50 shrink-0"
              title={t('chat.newConversation', '新会话')}
            >
              <Plus className="w-4 h-4" />
            </button>
          </div>
          {filteredSessions.length === 0 && (
            <div className="px-2 py-2">
              {searchQuery ? (
                <p className="text-xs text-[var(--text-secondary)] text-center py-4">{t('sessions.noResults', '无匹配会话')}</p>
              ) : (
                <button
                  onClick={handleNewSession}
                  className="w-full flex items-center gap-2 px-3 py-2 rounded-lg text-sm text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-secondary)] transition-colors border border-dashed border-[var(--border-color)] hover:border-[var(--accent-border)]"
                >
                  <Plus className="w-4 h-4 shrink-0" />
                  <span>{t('chat.startConversation', '开始对话')}</span>
                </button>
              )}
            </div>
          )}
          {filteredSessions.slice(0, displayCount).map((s) => (
            <SessionRow
              key={s.id}
              s={s}
              currentSessionId={currentSessionId}
              chatSessionId={chatSessionId}
              isBusy={isBusy}
            />
          ))}
          {displayCount < filteredSessions.length && (
            <button
              onClick={() => setDisplayCount((prev) => prev + 10)}
              className="w-full mt-1 px-3 py-1.5 text-xs text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-secondary)] rounded-lg transition-colors"
            >
              {t('common.loadMore', 'Load More')}
            </button>
          )}
        </div>
      )}

      {/* 折叠态：新建按钮 + 会话图标列表 */}
      {!sidebarOpen && (
        <>
          <div className="px-2 py-2 flex justify-center">
            <button
              onClick={handleNewSession}
              disabled={creating}
              className="p-1.5 rounded-lg text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-secondary)] transition-colors disabled:opacity-50"
              title={t('chat.newConversation', '新会话')}
            >
              <Plus className="w-4 h-4" />
            </button>
          </div>
          <div className="flex-1 overflow-y-auto px-2 py-1 flex flex-col items-center gap-0.5">
            {sessions.slice(0, displayCount).map((s) => {
              const label = s.name || (s.message_count === 0 ? t('sessions.newSession', '新会话') : s.id.slice(0, 8));
              return (
                <NavLink
                  key={s.id}
                  to={`/sessions/${s.id}`}
                  onMouseEnter={(e) => setTooltip({ label, y: (e.currentTarget as HTMLElement).getBoundingClientRect().top + 14 })}
                  onMouseLeave={() => setTooltip(null)}
                  className={({ isActive }) =>
                    `p-1.5 rounded-lg transition-colors ${
                      isActive
                        ? 'text-[var(--text-primary)] bg-[var(--bg-secondary)]'
                        : 'text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-secondary)]'
                    }`
                  }
                >
                  {s.is_starred
                    ? <Star className="w-[18px] h-[18px] text-[var(--accent-500)] fill-[var(--accent-500)]" />
                    : <MessageSquare className="w-[18px] h-[18px]" />
                  }
                </NavLink>
              );
            })}
          </div>
        </>
      )}

      {/* 底部区域 */}
      <div className="border-t border-[var(--border-color)]">
        {/* 偏好设置 + 指南 */}
        <nav className="px-2 py-2 space-y-0.5">
          {BOTTOM_NAV.map((item) => (
            <NavItem key={item.path} item={item} sidebarOpen={sidebarOpen} t={t} />
          ))}
          {/* 管理后台入口 */}
          <NavLink
            to="/admin"
            className="flex items-center gap-3 px-3 py-2 text-sm rounded-lg transition-colors text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-secondary)]"
            title={!sidebarOpen ? t('nav.admin') : undefined}
          >
            <ExternalLink className="w-[18px] h-[18px] shrink-0" />
            {sidebarOpen && <span>{t('nav.admin')}</span>}
          </NavLink>
        </nav>

        {/* 连接状态 */}
        <div className={`px-4 py-2.5 border-t border-[var(--border-color)] text-xs text-[var(--text-secondary)] ${sidebarOpen ? '' : 'px-2 text-center'}`}>
          <div className="flex items-center gap-2 justify-center">
            <span className={`w-1.5 h-1.5 rounded-full ${connected ? 'bg-emerald-500' : 'bg-red-500'}`} />
            {sidebarOpen && <span>{connected ? t('common.local') : t('common.disconnected', '已断开')}</span>}
          </div>
        </div>
      </div>
      {/* fixed tooltip：绕过父容器 overflow-hidden */}
      {!sidebarOpen && tooltip && (
        <div
          className="fixed left-16 z-[9999] px-2 py-1 text-xs rounded-md shadow-md pointer-events-none whitespace-nowrap bg-[var(--bg-card)] border border-[var(--border-color)] text-[var(--text-primary)]"
          style={{ top: tooltip.y }}
        >
          {tooltip.label}
        </div>
      )}
    </aside>
  );
}
