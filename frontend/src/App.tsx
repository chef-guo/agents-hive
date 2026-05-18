import { lazy, Suspense, useEffect } from 'react';
import { BrowserRouter, Routes, Route, Navigate, Outlet } from 'react-router-dom';
import { AppShell } from './layouts/AppShell';
import { AdminShell } from './layouts/AdminShell';
import { Login } from './pages/Login';
import { AuthCallback } from './pages/AuthCallback';
import { RegisterInvite } from './pages/RegisterInvite';
import { Register } from './pages/Register';
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
const InviteCodes = lazy(() => import('./pages/admin/InviteCodes').then(({ InviteCodes }) => ({ default: InviteCodes })));
const KnowledgeBase = lazy(() => import('./pages/admin/KnowledgeBase').then(({ KnowledgeBase }) => ({ default: KnowledgeBase })));
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
            <Route path="/register/invite" element={<RegisterInvite />} />
            <Route path="/register" element={<Register />} />

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

              {/* 管理后台路由（整棵 /admin 树统一 AdminGuard） */}
              <Route element={<AdminGuard><AdminShell /></AdminGuard>}>
                <Route path="/admin" element={<Dashboard />} />
                <Route path="/admin/agents" element={<Agents />} />
                <Route path="/admin/scheduled-tasks" element={<ScheduledTasks />} />
                <Route path="/admin/skills" element={<Skills />} />
                <Route path="/admin/settings" element={<AdminSettings />} />
                <Route path="/admin/guide" element={<Guide key="admin-guide" variant="admin" />} />
                <Route path="/admin/users" element={<UserList />} />
                <Route path="/admin/usage" element={<UsageStats />} />
                <Route path="/admin/auth-providers" element={<AuthProviders />} />
                <Route path="/admin/invite-codes" element={<InviteCodes />} />
                <Route path="/admin/kb" element={<KnowledgeBase />} />
                <Route path="/admin/prompts" element={<PromptManager />} />
                <Route path="/admin/quality-candidates" element={<QualityCandidates />} />
                <Route path="/admin/quality-workbench" element={<QualityWorkbench />} />
                <Route path="/admin/memory-governance" element={<MemoryGovernance />} />
                <Route path="/admin/auto-optimization" element={<AutoOptimization />} />
                <Route path="/admin/multi-agent" element={<MultiAgentEcosystem />} />
                <Route path="/admin/llm" element={<LLMProviders />} />
              </Route>
            </Route>
          </Routes>
        </Suspense>
      </BrowserRouter>
    </ErrorBoundary>
  );
}
