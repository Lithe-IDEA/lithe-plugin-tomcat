"""Static WebBridge mock: not a native Lithe UI test."""
from pathlib import Path
import unittest

try:
    from playwright.sync_api import sync_playwright
except ImportError:
    sync_playwright = None

ROOT = Path(__file__).resolve().parent.parent
BRIDGE_STUB = r"""
window.lithe = {
  version:0,
  context:{locale:'zh-CN', platform:'macos', theme:'dark'},
  request:async(method,p)=>{
    if(method!=='functions.invoke') throw Error('Unexpected method: '+method);
    if(p.function==='tomcat.get') return {value:{workspaceRoot:'/workspace',running:false,
      config:{home:'/opt/apache-tomcat',base:'',war:'target/demo.war',context:'/demo',debugPort:5005}}};
    if(p.function==='tomcat.save') return {value:{saved:true}};
    if(p.function==='tomcat.discover') return {value:{modules:[{name:'old-web',path:'old-web',model:'eclipse',facet:'Eclipse WTP Web 3.1',webRoot:'old-web/WebContent',descriptor:'old-web/WebContent/WEB-INF/web.xml',warCandidates:['old-web/target/old-web.war'],explodedCandidates:['old-web/target/exploded']} ]}};
    if(p.function==='tomcat.planDeploy') return {value:{source:'/workspace/target/demo.war',
      target:'/opt/apache-tomcat/webapps/demo.war',action:'create',
      warning:'Stop Tomcat before deployment.',confirmationToken:'a'.repeat(64)}};
    if(p.function==='tomcat.deploy') return {value:{deployed:true,target:'/opt/apache-tomcat/webapps/demo.war'}};
    throw Error('Unsupported function '+p.function);
  }
};
"""


@unittest.skipIf(sync_playwright is None, 'Playwright is not installed')
class WebPanelTests(unittest.TestCase):
    def test_local_web_panel_preview_and_confirmation(self):
        with sync_playwright() as p:
            browser = p.chromium.launch(executable_path='/usr/bin/chromium', headless=True, args=['--no-sandbox'])
            try:
                page = browser.new_page(viewport={'width': 1100, 'height': 900}, device_scale_factor=1)
                errors = []
                page.on('pageerror', lambda e: errors.append(str(e)))
                # Browser policy blocks file://. Load exact static sources in an
                # isolated page; UI requests use a stub, never real deployment.
                html = (ROOT/'ui/index.html').read_text(encoding='utf-8')
                html = html.replace('<script src="app.js" defer></script>', '')
                page.set_content(html)
                page.evaluate(BRIDGE_STUB)
                page.add_style_tag(content=(ROOT/'ui/style.css').read_text(encoding='utf-8'))
                page.add_script_tag(content=(ROOT/'ui/app.js').read_text(encoding='utf-8'))
                page.wait_for_function("document.querySelector('#home').value === '/opt/apache-tomcat'")
                self.assertTrue(page.get_by_text('服务器配置', exact=True).is_visible())
                page.wait_for_function("document.querySelector('#modules').textContent.includes('Eclipse WTP')")
                self.assertTrue(page.get_by_text('Web 模块 / Facet', exact=True).is_visible())
                page.locator('#previewDeploy').click()
                self.assertTrue(page.locator('#confirmBox').is_visible())
                self.assertIn('demo.war', page.locator('#planTarget').inner_text())
                out = ROOT/'.artifacts'/'panel-preview.png'
                out.parent.mkdir(exist_ok=True)
                page.screenshot(path=str(out), full_page=True)
                page.locator('#confirmDeploy').click()
                page.wait_for_function("document.querySelector('#notice').textContent.includes('部署完成')")
                self.assertFalse(page.locator('#confirmBox').is_visible())
                self.assertEqual(errors, [])
            finally:
                browser.close()

    def test_facet_selects_exploded_and_disables_auto_watcher(self):
        with sync_playwright() as p:
            browser = p.chromium.launch(executable_path='/usr/bin/chromium', headless=True, args=['--no-sandbox'])
            try:
                page = browser.new_page(viewport={'width': 1050, 'height': 860})
                errors = []
                page.on('pageerror', lambda e: errors.append(str(e)))
                html = (ROOT / 'ui/index.html').read_text(encoding='utf-8').replace('<script src="app.js" defer></script>', '')
                page.set_content(html)
                page.evaluate(BRIDGE_STUB)
                page.add_style_tag(content=(ROOT / 'ui/style.css').read_text(encoding='utf-8'))
                page.add_script_tag(content=(ROOT / 'ui/app.js').read_text(encoding='utf-8'))
                page.wait_for_function("document.querySelector('#modules').textContent.includes('Eclipse WTP')")
                page.locator('#watchChanges').check()
                page.locator('#artifact').select_option('exploded')
                self.assertTrue(page.locator('#explodedRow').is_visible())
                self.assertFalse(page.locator('#warRow').is_visible())
                self.assertFalse(page.locator('#watchChanges').is_checked())
                self.assertTrue(page.locator('#watchChanges').is_disabled())
                self.assertEqual(errors, [])
            finally:
                browser.close()
