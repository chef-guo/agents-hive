import { useState, useRef, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { ArrowUp, Paperclip, Brain, X, AlertCircle, Cpu, ChevronDown, Square } from 'lucide-react';
import type { FileAttachment } from '../../types/api';
import { useChatStore } from '../../store/chat';
import { useNodeClient } from '../../hooks/useNodeClient';
import { useToastStore } from '../../store/toast';
import { AttachmentIcon } from './AttachmentIcon';
import { formatFileSize } from './attachmentUtils';

const MAX_FILE_SIZE = 25 * 1024 * 1024; // 25MB
const MAX_FILE_COUNT = 10;

const SUPPORTED_MIME_TYPES = new Set([
  'image/png', 'image/jpeg', 'image/gif', 'image/webp', 'image/svg+xml',
  'audio/mpeg', 'audio/wav', 'audio/mp4', 'audio/webm', 'audio/ogg',
  'video/mp4', 'video/mpeg',
  'application/pdf',
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  'application/vnd.openxmlformats-officedocument.presentationml.presentation',
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  'application/msword',
  'application/vnd.ms-excel',
  'application/vnd.ms-powerpoint',
  'text/plain', 'text/markdown', 'text/csv', 'text/xml', 'text/html',
  'application/json', 'application/xml', 'application/yaml',
]);

// 代码文件扩展名 → 视为 text/plain
const CODE_EXTENSIONS = new Set([
  '.go', '.py', '.js', '.ts', '.tsx', '.jsx', '.java', '.c', '.cpp', '.rs', '.sh',
  '.rb', '.lua', '.php', '.swift', '.kt', '.scala', '.r',
  '.md', '.json', '.csv', '.xml', '.yaml', '.yml', '.log', '.txt', '.toml', '.ini',
  '.html', '.css', '.scss', '.less', '.sql', '.graphql', '.proto',
]);

const FILE_ACCEPT = "image/*,audio/*,video/mp4,video/mpeg,.pdf,.doc,.docx,.ppt,.pptx,.xls,.xlsx,.txt,.md,.json,.csv,.xml,.yaml,.yml,.log,.go,.py,.js,.ts,.tsx,.jsx,.java,.c,.cpp,.rs,.sh,.rb,.lua,.php,.swift,.kt,.scala,.r,.toml,.ini,.html,.css,.scss,.sql,.graphql,.proto";

function getFileMimeType(file: globalThis.File): string {
  if (file.type && file.type !== 'application/octet-stream') {
    return file.type;
  }
  const ext = '.' + file.name.split('.').pop()?.toLowerCase();
  if (CODE_EXTENSIONS.has(ext)) return 'text/plain';
  return file.type || 'application/octet-stream';
}

export interface SendOptions {
  attachments?: FileAttachment[];
  deepThinking?: boolean;
}

interface Props {
  sessionId: string;
  onSend: (content: string, options?: SendOptions) => void;
  onRequireSession?: () => Promise<string>;
  onStop?: () => void;
  disabled?: boolean;
  placeholder?: string;
  notice?: string;
  allowAttachments?: boolean;
  allowDeepThinking?: boolean;
}

export function ChatInput({
  sessionId,
  onSend,
  onRequireSession,
  onStop,
  disabled,
  placeholder,
  notice,
  allowAttachments = true,
  allowDeepThinking = true,
}: Props) {
  const { t } = useTranslation();
  const [value, setValue] = useState('');
  const [deepThinking, setDeepThinking] = useState(false);
  const [files, setFiles] = useState<FileAttachment[]>([]);
  const [fileError, setFileError] = useState<string | null>(null);
  const [isDragOver, setIsDragOver] = useState(false);
  const [modelOpen, setModelOpen] = useState(false);
  const modelRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const isComposingRef = useRef(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const dropZoneRef = useRef<HTMLDivElement>(null);
  const client = useNodeClient();
  const addToast = useToastStore((s) => s.addToast);
  const availableModels = useChatStore((s) => s.availableModels);
  const activeModel = useChatStore((s) => s.activeModel);

  // 自动调整输入框高度
  useEffect(() => {
    const ta = textareaRef.current;
    if (ta) {
      ta.style.height = 'auto';
      ta.style.height = Math.min(ta.scrollHeight, 240) + 'px';
    }
  }, [value]);

  // 自动清除错误消息
  useEffect(() => {
    if (fileError) {
      const timer = setTimeout(() => setFileError(null), 4000);
      return () => clearTimeout(timer);
    }
  }, [fileError]);

  // 点击外部关闭下拉
  useEffect(() => {
    const handleClick = (e: MouseEvent) => {
      if (modelRef.current && !modelRef.current.contains(e.target as Node)) setModelOpen(false);
    };
    document.addEventListener('mousedown', handleClick);
    return () => document.removeEventListener('mousedown', handleClick);
  }, []);

  const handleSwitchModel = useCallback(async (name: string) => {
    try {
      const targetSessionId = sessionId || await onRequireSession?.();
      if (!targetSessionId) {
        throw new Error(t('sessions.newSession', '新会话'));
      }
      await client.switchModel(targetSessionId, name);
      useChatStore.setState((s) => ({
        activeModel: name,
        availableModels: s.availableModels.map((model) => ({
          ...model,
          is_active: model.name === name,
        })),
      }));
    } catch (e) {
      addToast('error', e instanceof Error ? e.message : t('common.error'));
    }
    setModelOpen(false);
  }, [client, sessionId, onRequireSession, addToast, t]);

  const processFiles = useCallback((inputFiles: globalThis.File[]) => {
    const remaining = MAX_FILE_COUNT - files.length;
    if (remaining <= 0) {
      setFileError(t('chat.tooManyFiles'));
      return;
    }

    const filesToProcess = inputFiles.slice(0, remaining);
    if (inputFiles.length > remaining) {
      setFileError(t('chat.tooManyFiles'));
    }

    filesToProcess.forEach((file) => {
      if (file.size > MAX_FILE_SIZE) {
        setFileError(`${file.name}: ${t('chat.fileTooLarge')}`);
        return;
      }

      const mimeType = getFileMimeType(file);
      const ext = '.' + file.name.split('.').pop()?.toLowerCase();
      if (!SUPPORTED_MIME_TYPES.has(mimeType) && !CODE_EXTENSIONS.has(ext)) {
        setFileError(`${file.name}: ${t('chat.unsupportedFile')}`);
        return;
      }

      const reader = new FileReader();
      reader.onload = () => {
        const base64 = (reader.result as string).split(',')[1];
        setFiles((prev) => {
          if (prev.length >= MAX_FILE_COUNT) return prev;
          return [...prev, {
            filename: file.name,
            mime_type: mimeType,
            data: base64,
            size: file.size,
          }];
        });
      };
      reader.onerror = () => {
        setFileError(`${file.name}: ${t('chat.fileReadError', '文件读取失败')}`);
      };
      reader.readAsDataURL(file);
    });
  }, [files.length, t]);

  const handleFileChange = useCallback((e: React.ChangeEvent<HTMLInputElement>) => {
    if (!allowAttachments) return;
    const selectedFiles = e.target.files;
    if (!selectedFiles) return;
    processFiles(Array.from(selectedFiles));
    e.target.value = '';
  }, [allowAttachments, processFiles]);

  // 拖拽事件处理
  const handleDragOver = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setIsDragOver(true);
  }, []);

  const handleDragLeave = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    // 只有真正离开 dropZone 才取消高亮
    if (dropZoneRef.current && !dropZoneRef.current.contains(e.relatedTarget as Node)) {
      setIsDragOver(false);
    }
  }, []);

  const handleDrop = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setIsDragOver(false);
    if (disabled || !allowAttachments) return;
    const droppedFiles = Array.from(e.dataTransfer.files);
    if (droppedFiles.length > 0) {
      processFiles(droppedFiles);
    }
  }, [allowAttachments, disabled, processFiles]);

  const removeFile = useCallback((index: number) => {
    setFiles((prev) => prev.filter((_, i) => i !== index));
  }, []);

  const handleSubmit = () => {
    const trimmed = value.trim();
    if (!trimmed && files.length === 0) return;
    if (disabled) return;
    const options: SendOptions = {};
    if (allowAttachments && files.length > 0) options.attachments = files;
    if (allowDeepThinking && deepThinking) options.deepThinking = true;
    onSend(trimmed, Object.keys(options).length > 0 ? options : undefined);
    setValue('');
    setFiles([]);
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey && !isComposingRef.current) {
      e.preventDefault();
      handleSubmit();
    }
  };

  // 粘贴图片支持
  const handlePaste = useCallback((e: React.ClipboardEvent) => {
    const items = e.clipboardData.items;
    const imageFiles: globalThis.File[] = [];
    for (let i = 0; i < items.length; i++) {
      if (items[i].type.startsWith('image/')) {
        const file = items[i].getAsFile();
        if (file) imageFiles.push(file);
      }
    }
    if (imageFiles.length > 0) {
      if (!allowAttachments) return;
      processFiles(imageFiles);
    }
  }, [allowAttachments, processFiles]);

  const canSend = (value.trim().length > 0 || files.length > 0) && !disabled;

  return (
    <div className="pb-5 pt-2 shrink-0">
      <div className="max-w-4xl mx-auto px-4">
        {notice && (
          <div className="mb-2 flex items-center gap-2 px-3 py-2 bg-amber-50 dark:bg-amber-900/10 border border-amber-200 dark:border-amber-800 rounded-xl text-xs text-amber-700 dark:text-amber-300">
            <AlertCircle className="w-3.5 h-3.5 shrink-0" />
            <span>{notice}</span>
          </div>
        )}
        {/* 文件错误提示 */}
        {fileError && (
          <div className="mb-2 flex items-center gap-2 px-3 py-2 bg-red-50 dark:bg-red-900/10 border border-red-200 dark:border-red-800 rounded-xl text-xs text-red-600 dark:text-red-400">
            <AlertCircle className="w-3.5 h-3.5 shrink-0" />
            <span className="flex-1">{fileError}</span>
            <button onClick={() => setFileError(null)} className="text-red-400 hover:text-red-600">
              <X className="w-3 h-3" />
            </button>
          </div>
        )}
        <div
          ref={dropZoneRef}
          onDragOver={handleDragOver}
          onDragLeave={handleDragLeave}
          onDrop={handleDrop}
          className={`relative bg-[var(--bg-card)] border rounded-2xl shadow-sm shadow-black/[0.04] dark:shadow-black/[0.2] transition-all focus-within:border-[var(--accent-border)] focus-within:ring-2 focus-within:ring-[var(--accent-subtle)] ${
            isDragOver
              ? 'border-[var(--accent-500)] dark:border-[var(--accent-300)] ring-2 ring-[var(--accent-100)] dark:ring-[var(--accent-border)] bg-[var(--accent-50)]/30 dark:bg-[var(--accent-light)]'
              : 'border-[var(--border-color)]'
          }`}
        >
          {/* 拖拽覆盖层 */}
          {isDragOver && (
            <div className="absolute inset-0 flex items-center justify-center bg-[var(--accent-subtle)] rounded-2xl z-10 pointer-events-none">
              <div className="flex items-center gap-2 text-[var(--accent)] text-sm font-medium">
                <Paperclip className="w-5 h-5" />
                <span>{t('chat.dropFiles')}</span>
              </div>
            </div>
          )}
          {/* 隐藏的文件输入 */}
          <input
            ref={fileInputRef}
            type="file"
            multiple
            accept={FILE_ACCEPT}
            onChange={handleFileChange}
            className="hidden"
          />
          {/* 文本输入区域 */}
          <textarea
            ref={textareaRef}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={handleKeyDown}
            onCompositionStart={() => { isComposingRef.current = true; }}
            onCompositionEnd={() => { isComposingRef.current = false; }}
            onPaste={handlePaste}
            placeholder={placeholder || t('chat.inputPlaceholder')}
            disabled={disabled}
            rows={4}
            className="w-full px-3 pt-2.5 pb-1 bg-transparent text-[13px] text-[var(--text-primary)] placeholder:text-[var(--text-secondary)] focus:outline-none resize-none disabled:opacity-50"
          />
          {/* 文件预览栏 */}
          {files.length > 0 && (
            <div className="flex flex-wrap gap-1.5 px-3 py-2 border-t border-[var(--border-color)]">
              {files.map((f, i) => (
                <div key={i} className="flex items-center gap-1.5 px-2 py-1 bg-[var(--bg-secondary)] rounded-lg text-xs">
                  {f.mime_type.startsWith('image/') ? (
                    <img
                      src={`data:${f.mime_type};base64,${f.data}`}
                      alt={f.filename}
                      className="w-8 h-8 rounded object-cover"
                    />
                  ) : (
                    <AttachmentIcon mimeType={f.mime_type} />
                  )}
                  <span className="truncate max-w-[120px] text-[var(--text-primary)]">{f.filename}</span>
                  <span className="text-[var(--text-secondary)]">{formatFileSize(f.size)}</span>
                  <button
                    onClick={() => removeFile(i)}
                    className="text-[var(--text-secondary)] hover:text-red-500 transition-colors"
                    title={t('chat.removeFile')}
                  >
                    <X className="w-3 h-3" />
                  </button>
                </div>
              ))}
            </div>
          )}
          {/* 底部工具栏 */}
          <div className="flex items-center justify-between px-3 pb-2.5 pt-1">
            <div className="flex items-center gap-1">
              {/* Model 选择器 */}
              {availableModels.length > 0 && (
                <div ref={modelRef} className="relative">
                  <button
                    type="button"
                    onClick={() => setModelOpen(!modelOpen)}
                    className="flex items-center gap-1 px-2 py-1 rounded-lg text-xs text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-hover)] transition-colors"
                    title={t('chat.model')}
                  >
                    <Cpu className="w-3.5 h-3.5" />
                    <span>{activeModel || t('chat.model')}</span>
                    <ChevronDown className="w-3 h-3" />
                  </button>
                  {modelOpen && (
                    <div className="absolute bottom-full left-0 mb-1 w-52 bg-[var(--bg-card)] border border-[var(--border-color)] rounded-xl shadow-lg z-50 py-1 max-h-60 overflow-y-auto">
                      {availableModels.map((m) => (
                        <button
                          key={m.name}
                          onClick={() => handleSwitchModel(m.name)}
                          className={`w-full text-left px-3 py-2 text-xs hover:bg-[var(--bg-hover)] transition-colors ${
                            m.is_active ? 'text-[var(--accent-600)] dark:text-[var(--accent-300)] font-medium' : 'text-[var(--text-primary)]'
                          }`}
                        >
                          <div className="font-medium">{m.name}</div>
                          <div className="text-[var(--text-secondary)] mt-0.5 truncate">{m.model}</div>
                        </button>
                      ))}
                    </div>
                  )}
                </div>
              )}
              {/* 分隔符 */}
              {availableModels.length > 0 && (
                <div className="w-px h-4 bg-[var(--border-color)] mx-0.5" />
              )}
              {/* 附件按钮 */}
              {allowAttachments && (
                <button
                  type="button"
                  onClick={() => fileInputRef.current?.click()}
                  className="p-1.5 rounded-lg text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-hover)] transition-colors"
                  title={t('chat.attachment')}
                >
                  <Paperclip className="w-4 h-4" />
                </button>
              )}
              {/* 深度思考开关 */}
              {allowDeepThinking && (
                <button
                  type="button"
                  onClick={() => setDeepThinking(!deepThinking)}
                  className={`flex items-center gap-1 px-2 py-1 rounded-lg text-xs transition-colors ${
                    deepThinking
                      ? 'text-[var(--accent-600)] dark:text-[var(--accent-300)] bg-[var(--accent-50)] dark:bg-[var(--accent-light)]'
                      : 'text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-hover)]'
                  }`}
                  title={t('chat.deepThinking')}
                >
                  <Brain className="w-3.5 h-3.5" />
                  <span>{t('chat.deepThinking')}</span>
                </button>
              )}
            </div>
            <div className="flex items-center gap-2">
              {value.trim().length > 0 && (
                <span className="text-[10px] text-[var(--text-secondary)]">
                  {value.trim().length}
                </span>
              )}
              {disabled && onStop ? (
                <button
                  onClick={onStop}
                  aria-label={t('chat.stop', '停止')}
                  className="p-2 rounded-xl transition-all bg-red-500 text-white hover:bg-red-600 shadow-sm"
                  title={t('chat.stop', '停止')}
                >
                  <Square className="w-4 h-4 fill-current" />
                </button>
              ) : (
                <button
                  onClick={handleSubmit}
                  disabled={!canSend}
                  aria-label={t('chat.send', '发送')}
                  className={`p-2 rounded-xl transition-all ${
                    canSend
                      ? 'bg-[var(--accent-600)] text-white hover:bg-[var(--accent-700)] shadow-sm'
                      : 'bg-[var(--border-color)] text-[var(--text-secondary)] opacity-40 cursor-not-allowed'
                  }`}
                >
                  <ArrowUp className="w-4 h-4" strokeWidth={2.5} />
                </button>
              )}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
