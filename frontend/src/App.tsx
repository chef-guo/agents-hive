import { lazy, Suspense, useEffect } from 'react';
import { BrowserRouter, Routes, Route, Navigate, Outlet } from 'react-router-dom';
import { AppShell } from './layouts/AppShell';
import { AdminShell } from './layouts/AdminShell';
import { Login } from './pages/Login';
import { AuthCallback } from './pages/AuthCallback';
import { useTheme } from './hooks/useTheme';
import { useLanguage } from './hooks/useLanguage';
import { useAppStore } from './store/app';
import { ErrorBoundary } from './components/common/ErrorBoundary';
import { AuthGuard } from './components/AuthGuard';
import { AdminGuard } from './components/AdminGuard';

const ChatLanding = lazy(() => import('./pages/ChatLanding').then(({ ChatLanding }) => ({ default: ChatLanding })));
const Dashboard = lazy(() => import('./pages/Dashboard').then(({ Dashboard }) => ({ default: Dashboard })));
const Chat = lazy(() => import('./pages/Chat').then(({ Chat }) => ({ default: Chat })));
const Agents = lazy(() => import('./pages/Agents').then(({ Agents }) => ({ default: Agents })));
const Skills = lazy(() => import('./pages/Skills').then(({ Skills }) => ({ default: Skills })));
const Guide = lazy(() => import('./pages/Guide').then(({ Guide }) => ({ default: Guide })));
const Settings = lazy(() => import('./pages/Settings').then(({ Settings }) => ({ default: Settings })));
const AdminSettings = lazy(() => import('./pages/AdminSettings').then(({ AdminSettings }) => ({ default: AdminSettings })));
const ScheduledTasks = lazy(() => import('./pages/ScheduledTasks').then(({ ScheduledTasks }) => ({ default: ScheduledTasks })));
const UserList = lazy(() => import('./pages/admin/UserList').then(({ UserList }) => ({ default: UserList })));
const UsageStats = lazy(() => import('./pages/admin/UsageStats').then(({ UsageStats }) => ({ default: UsageStats })));
const AuthProviders = lazy(() => import('./pages/admin/AuthProviders').then(({ AuthProviders }) => ({ default: AuthProviders })));
const PromptManager = lazy(() => import('./pages/admin/PromptManager').then(({ PromptManager }) => ({ default: PromptManager })));
const QualityCandidates = lazy(() => import('./pages/admin/QualityCandidates').then(({ QualityCandidates }) => ({ default: QualityCandidates })));
const QualityWorkbench = lazy(() => import('./pages/admin/qualityworkbench/QualityWorkbench').then(({ QualityWorkbench }) => ({ default: QualityWorkbench })));
const MemoryGovernance = lazy(() => import('./pages/admin/MemoryGovernance').then(({ MemoryGovernance }) => ({ default: MemoryGovernance })));
const AutoOptimization = lazy(() => import('./pages/admin/AutoOptimization').then(({ AutoOptimization }) => ({ default: AutoOptimization })));
const MultiAgentEcosystem = lazy(() => import('./pages/admin/MultiAgentEcosystem').then(({ MultiAgentEcosystem }) => ({ default: MultiAgentEcosystem })));
const LLMProviders = lazy(() => import('./pages/admin/LLMProviders').then(({ LLMProviders }) => ({ default: LLMProviders })));
const SessionReplay = lazy(() => import('./pages/SessionReplay').then(({ SessionReplay }) => ({ default: SessionReplay })));
const ReplayGallery = lazy(() => import('./pages/ReplayGallery').then(({ ReplayGallery }) => ({ default: ReplayGallery })));

function RouteFallback() {
  return (
    <div className="p-6 text-sm text-[var(--text-secondary)]">
      加载中...
    </div>
  );
}

export default function App() {
  // 初始化主题和语言 (hooks 内部执行副作用)
  useTheme();
  useLanguage();

  useEffect(() => {
    // 检测系统主题偏好和浏览器语言（仅在首次加载时）
    const stored = localStorage.getItem('app-storage');
    if (!stored) {
      // 没有保存的设置，使用系统默认
      const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
      useAppStore.getState().setTheme(prefersDark ? 'dark' : 'light');

      const browserLang = navigator.language.startsWith('zh') ? 'zh' : 'en';
      useAppStore.getState().setLanguage(browserLang);
    }
  }, []);

  return (
    <ErrorBoundary>
      <BrowserRouter>
        <Suspense fallback={<RouteFallback />}>
          <Routes>
            {/* 公开路由 */}
            <Route path="/login" element={<Login />} />
            <Route path="/auth/callback" element={<AuthCallback />} />

            {/* 受保护路由 — AuthGuard 包裹 */}
            <Route element={<AuthGuard><Outlet /></AuthGuard>}>
              <Route element={<AppShell />}>
                <Route path="/" element={<ChatLanding />} />
                <Route path="/sessions/:id" element={<Chat />} />
                <Route path="/replay" element={<ReplayGallery />} />
                <Route path="/guide" element={<Guide key="user-guide" variant="user" />} />
                <Route path="/settings" element={<Settings />} />
              </Route>

              {/* 回放页面（独立全屏布局，无 Sidebar） */}
              <Route path="/sessions/:id/replay" element={<SessionReplay />} />

              {/* 旧路由重定向到管理后台 */}
              <Route path="/agents" element={<Navigate to="/admin/agents" replace />} />
              <Route path="/skills" element={<Navigate to="/admin/skills" replace />} />

              {/* 管理后台路由 */}
              <Route element={<AdminShell />}>
                <Route path="/admin" element={<Dashboard />} />
                <Route path="/admin/agents" element={<Agents />} />
                <Route path="/admin/scheduled-tasks" element={<ScheduledTasks />} />
                <Route path="/admin/skills" element={<AdminGuard><Skills /></AdminGuard>} />
                <Route path="/admin/settings" element={<AdminGuard><AdminSettings /></AdminGuard>} />
                <Route path="/admin/guide" element={<Guide key="admin-guide" variant="admin" />} />
                {/* Admin-only 页面 */}
                <Route path="/admin/users" element={<AdminGuard><UserList /></AdminGuard>} />
                <Route path="/admin/usage" element={<AdminGuard><UsageStats /></AdminGuard>} />
                <Route path="/admin/auth-providers" element={<AdminGuard><AuthProviders /></AdminGuard>} />
                <Route path="/admin/prompts" element={<AdminGuard><PromptManager /></AdminGuard>} />
                <Route path="/admin/quality-candidates" element={<AdminGuard><QualityCandidates /></AdminGuard>} />
                <Route path="/admin/quality-workbench" element={<AdminGuard><QualityWorkbench /></AdminGuard>} />
                <Route path="/admin/memory-governance" element={<AdminGuard><MemoryGovernance /></AdminGuard>} />
                <Route path="/admin/auto-optimization" element={<AdminGuard><AutoOptimization /></AdminGuard>} />
                <Route path="/admin/multi-agent" element={<AdminGuard><MultiAgentEcosystem /></AdminGuard>} />
                <Route path="/admin/llm" element={<AdminGuard><LLMProviders /></AdminGuard>} />
              </Route>
            </Route>
          </Routes>
        </Suspense>
      </BrowserRouter>
    </ErrorBoundary>
  );
}
