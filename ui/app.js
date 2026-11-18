'use strict';
// All UI belongs to this plugin; do not interpolate project-controlled data
// into HTML. The host owns the window, file picker and Run/Debug sessions.
const bridge=window.lithe;
const zh=String(bridge?.context?.locale||navigator.language).toLowerCase().startsWith('zh');
const words={
 subtitle:['Local Servlet container','本机 Servlet 容器'],webModules:['Web modules / Facets','Web 模块 / Facet'],rescan:['Rescan','重新扫描'],
 webHelp:['The Java plugin owns SDKs, classpaths and libraries. This view discovers deployable Web facets and artifacts.','SDK、类路径和库由 Java 插件管理。本页仅识别可部署的 Web Facet 与产物。'],
 configuration:['Configuration','服务器配置'],home:['Tomcat installation directory *','Tomcat 安装目录 *'],base:['Catalina base (optional)','Catalina Base（可选）'],
 artifact:['Artifact type','部署产物类型'],kindWar:['WAR archive','WAR 压缩包'],kindExploded:['Exploded web application','Exploded WAR（展开目录）'],
 war:['WAR file (relative to workspace)','WAR 文件（相对工作区）'],exploded:['Exploded web directory (relative to workspace)','Web 展开目录（相对工作区）'],
 browse:['Browse','浏览'],context:['Context path','应用访问路径'],port:['Local JDWP port','本地 JDWP 端口'],
 save:['Save configuration','保存配置'],inspect:['Inspect installation','检查安装'],server:['Server','服务器'],
 start:['Start','启动'],debug:['Debug (Java attach)','调试（自动附加 Java）'],stop:['Stop','停止'],
 debugHelp:['Lithe starts a localhost-only JDWP listener and attaches the Java Support debugger. The Java plugin must be installed.','Lithe 将启动仅监听本机的 JDWP 并附加 Java Support 调试器；需要安装 Java 插件。'],
 watchChanges:['Auto-redeploy updated WAR (opt-in)','WAR 变化后自动重部署（需主动启用）'],watchHelp:['Requires a first confirmed deployment, a managed Tomcat run, and autoDeploy=true in server.xml. Conflicts disable monitoring.','需先确认部署一次、由 Lithe 启动 Tomcat，并在 server.xml 中启用 autoDeploy；出现文件冲突会停止监控。'],
 deployment:['Web deployment','Web 应用部署'],
 deployHelp:['Stop Tomcat first. WAR and exploded artifacts must be in this workspace; review the destination before replacing.','请先停止 Tomcat。WAR 或展开目录必须位于当前工作区；替换前请检查目标位置。'],
 previewDeploy:['Preview deployment','预览部署'],source:['Source','源文件'],destination:['Destination','目标位置'],operation:['Operation','操作'],confirmDeploy:['Confirm deployment','确认部署'],cancel:['Cancel','取消'],refresh:['Refresh','刷新']
};
const tr=(en,cn)=>zh?cn:en;
const $=id=>document.getElementById(id);
for(const e of document.querySelectorAll('[data-i18n]')){const pair=words[e.dataset.i18n];if(pair)e.textContent=pair[Number(zh)]}
document.documentElement.lang=zh?'zh-CN':'en';
let workspaceRoot='',saved=null,plan=null,busy=false,running=false;
function info(message,isError=false){$('notice').textContent=String(message||'');$('notice').classList.toggle('error',isError)}
function toggleArtifact(){const exploded=$('artifact').value==='exploded';$('explodedRow').hidden=!exploded;$('warRow').hidden=exploded;$('watchChanges').disabled=exploded||running;if(exploded)$('watchChanges').checked=false;closePlan()}
function setBusy(v){busy=v;for(const id of ['refresh','discover','save','inspect','start','debug','stop','previewDeploy','confirmDeploy','pick']){$(id).disabled=busy || (id==='stop'&&!running) || (['start','debug'].includes(id)&&running) || (['previewDeploy','confirmDeploy'].includes(id)&&running&&!saved?.watchChanges) || (id==='save'&&running)}for(const id of ['home','base','war','exploded','artifact','context','port','watchChanges']){$(id).disabled=busy||running||(id==='watchChanges'&&$('artifact').value==='exploded')}}
function collect(){const n=Number($('port').value);return {home:$('home').value,base:$('base').value,artifact:$('artifact').value,war:$('war').value,exploded:$('exploded').value,context:$('context').value,debugPort:Number.isInteger(n)?n:0,watchChanges:$('watchChanges').checked}}
function render(c){for(const id of ['home','base','war','exploded','context'])$(id).value=c?.[id]||(id==='context'?'/':'');$('port').value=c?.debugPort||5005;$('artifact').value=c?.artifact||'war';$('watchChanges').checked=!!c?.watchChanges;toggleArtifact()}
function closePlan(){plan=null;$('confirmBox').hidden=true}
async function invoke(id,params={}){if(!bridge?.request)throw Error(tr('Lithe WebBridge is missing.','Lithe 网页桥不可用。'));const reply=await bridge.request('functions.invoke',{function:id,params});return reply.value}
async function task(fn){if(busy)return;setBusy(true);info('');try{const result=await fn();if(result)info(result)}catch(err){info(err?.message||String(err),true)}finally{setBusy(false)}}
async function refresh(){const r=await invoke('tomcat.get');workspaceRoot=r.workspaceRoot||'';saved=r.config||{};render(saved);running=!!r.running;$('runStatus').textContent=(running?tr('Running in Lithe: ','Lithe 运行中：')+r.runID:tr('No managed Tomcat session','没有由当前插件启动的 Tomcat 会话'))+(r.watching?tr(' · auto redeploy enabled',' · 已开启自动重部署'):'')+(r.watchError?'\n'+r.watchError:'');closePlan()}
async function save(){closePlan();await invoke('tomcat.save',{config:collect()});saved=collect();return tr('Configuration saved.','配置已保存。')}
function elt(tag,cls,body){const e=document.createElement(tag);if(cls)e.className=cls;if(body)e.textContent=body;return e}
async function discover(){const out=await invoke('tomcat.discover');const panel=$('modules');panel.replaceChildren();if(!out.modules?.length){panel.append(elt('p','muted',tr('No local Web facets or artifacts found.','没有发现本地 Web Facet 或部署产物。')));return ''}
 for(const m of out.modules){const card=elt('article','module');card.append(elt('h3','',m.name+'  ·  '+m.model));card.append(elt('p','',tr('Module: ','模块：')+(m.path||'.')));
   if(m.facet)card.append(elt('p','',tr('Facet: ','Facet：')+m.facet));
   if(m.webRoot)card.append(elt('p','',tr('Web root: ','Web 根目录：')+m.webRoot));
   if(m.descriptor)card.append(elt('p','', 'web.xml: '+m.descriptor));
   const actions=elt('div','actions');
   const choices=[...(m.warCandidates||[]).map(p=>['war',p]),...(m.explodedCandidates||[]).map(p=>['exploded',p])];
   if(m.webRoot)choices.push(['exploded',m.webRoot]);
   for(const [kind,path] of choices.slice(0,8)){const btn=elt('button','secondary',tr('Use ','使用 ')+(kind==='war'?'WAR: ':'Web: ')+path.split('/').slice(-1)[0]);btn.type='button';btn.disabled=running;btn.addEventListener('click',()=>{ $('artifact').value=kind;toggleArtifact();$(kind).value=path;if($('context').value==='/')$('context').value='/'+m.name.replace(/[^A-Za-z0-9_-]/g,'').slice(0,79);closePlan();info(tr('Selected artifact; inspect and save before deploying.','已选择部署产物；请先检查并保存配置。'));});actions.append(btn)}
   card.append(actions);panel.append(card)}return ''}
$('artifact').addEventListener('change',toggleArtifact);
for(const id of ['home','base','war','exploded','context','port','watchChanges'])$(id).addEventListener('input',closePlan);
$('refresh').addEventListener('click',()=>task(async()=>{await refresh();return tr('Refreshed.','已刷新。')}));
$('discover').addEventListener('click',()=>task(async()=>{await discover();return tr('Web modules scanned.','Web 模块扫描完成。')}));
$('save').addEventListener('click',()=>task(save));
$('inspect').addEventListener('click',()=>task(async()=>{await save();const x=await invoke('tomcat.inspect');return tr('Tomcat installation valid: ','Tomcat 检查通过：')+x.home+'\nBase: '+x.base+'\nWebapps: '+x.webapps+'\nautoDeploy: '+(x.server?.xmlValid?x.server?.autoDeploy:'unknown')+'\nHTTP ports: '+(x.server?.httpPorts||[]).join(', ')}));
$('start').addEventListener('click',()=>task(async()=>{await save();const x=await invoke('tomcat.start',{debug:false});running=true;$('runStatus').textContent=tr('Running in Lithe','Lithe 已启动');return 'Run ID: '+x.runID}));
$('debug').addEventListener('click',()=>task(async()=>{await save();const x=await invoke('tomcat.start',{debug:true});running=true;$('runStatus').textContent=tr('Debugging','调试中');return tr('Started and requested Java attach on 127.0.0.1:','已启动并请求 Java 调试器附加 127.0.0.1:')+x.debugPort}));
$('stop').addEventListener('click',()=>task(async()=>{const x=await invoke('tomcat.stop');return tr('Stop requested: ','已请求停止：')+x.runID}));
$('previewDeploy').addEventListener('click',()=>task(async()=>{if(!running)await save();plan=await invoke('tomcat.planDeploy');$('planSource').textContent=plan.source;$('planTarget').textContent=plan.target;$('planArtifact').textContent=plan.artifact+(plan.files?' ('+plan.files+' files)':'');$('planAction').textContent=plan.action==='replace'?tr('Replace existing deployment','替换现有部署'):tr('New deployment','新建部署');$('planWarning').textContent=plan.warning;$('confirmBox').hidden=false;return tr('Review target and confirm.','请检查目标位置后明确确认。')}));
$('confirmDeploy').addEventListener('click',()=>task(async()=>{if(!plan)throw Error(tr('Preview first.','请先预览。'));const token=plan.confirmationToken;closePlan();const r=await invoke('tomcat.deploy',{confirmationToken:token});return tr('Deployed: ','部署完成：')+r.target}));
$('cancelDeploy').addEventListener('click',closePlan);
$('pick').addEventListener('click',()=>task(async()=>{const r=await bridge.request('dialog.openFile',{title:tr('Select WAR from workspace','从工作区选择 WAR'),extensions:['war']});if(!r?.path)return '';const root=workspaceRoot.replace(/\\/g,'/').replace(/\/$/,'');const full=r.path.replace(/\\/g,'/');const fold=bridge?.context?.platform==='windows';const a=fold?root.toLowerCase():root,b=fold?full.toLowerCase():full;if(!root||!b.startsWith(a+'/'))throw Error(tr('The WAR must be inside the workspace.','WAR 文件必须位于当前工作区。'));$('war').value=full.slice(root.length+1);closePlan();return ''}));
setBusy(false);task(async()=>{await refresh();await discover();return ''});
