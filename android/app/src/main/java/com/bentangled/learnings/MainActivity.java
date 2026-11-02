package com.bentangled.learnings;

import android.app.Activity;
import android.content.ActivityNotFoundException;
import android.content.Intent;
import android.graphics.Color;
import android.net.Uri;
import android.os.Bundle;
import android.view.ViewGroup;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.Toast;

public final class MainActivity extends Activity {
    private static final String GUIDE_URL = "file:///android_asset/index.html";
    private static final String ASSET_PREFIX = "file:///android_asset/";

    private WebView guide;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        getWindow().setStatusBarColor(Color.rgb(7, 11, 18));
        getWindow().setNavigationBarColor(Color.rgb(7, 11, 18));

        guide = new WebView(this);
        guide.setLayoutParams(new ViewGroup.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT));
        guide.setBackgroundColor(Color.rgb(7, 11, 18));

        WebSettings settings = guide.getSettings();
        settings.setJavaScriptEnabled(true);
        settings.setDomStorageEnabled(true);
        settings.setDefaultTextEncodingName("UTF-8");
        settings.setAllowFileAccess(true);
        settings.setAllowContentAccess(false);
        settings.setAllowFileAccessFromFileURLs(false);
        settings.setAllowUniversalAccessFromFileURLs(false);
        settings.setMixedContentMode(WebSettings.MIXED_CONTENT_NEVER_ALLOW);
        settings.setBuiltInZoomControls(true);
        settings.setDisplayZoomControls(false);

        guide.setWebChromeClient(new WebChromeClient());
        guide.setWebViewClient(new WebViewClient() {
            @Override
            public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                return handleNavigation(request.getUrl());
            }

            @Override
            @SuppressWarnings("deprecation")
            public boolean shouldOverrideUrlLoading(WebView view, String url) {
                return handleNavigation(Uri.parse(url));
            }
        });

        setContentView(guide);
        if (savedInstanceState != null && guide.restoreState(savedInstanceState) != null) {
            return;
        }
        guide.loadUrl(GUIDE_URL);
    }

    private boolean handleNavigation(Uri uri) {
        String url = uri.toString();
        if (url.startsWith(ASSET_PREFIX) || "about".equals(uri.getScheme())) {
            return false;
        }

        if ("https".equals(uri.getScheme()) || "http".equals(uri.getScheme())) {
            try {
                startActivity(new Intent(Intent.ACTION_VIEW, uri));
            } catch (ActivityNotFoundException exception) {
                Toast.makeText(this, "No browser is available to open this link.", Toast.LENGTH_SHORT).show();
            }
        }
        return true;
    }

    @Override
    public void onBackPressed() {
        if (guide != null && guide.canGoBack()) {
            guide.goBack();
            return;
        }
        super.onBackPressed();
    }

    @Override
    protected void onPause() {
        if (guide != null) {
            guide.onPause();
        }
        super.onPause();
    }

    @Override
    protected void onResume() {
        super.onResume();
        if (guide != null) {
            guide.onResume();
        }
    }

    @Override
    protected void onSaveInstanceState(Bundle outState) {
        if (guide != null) {
            guide.saveState(outState);
        }
        super.onSaveInstanceState(outState);
    }

    @Override
    protected void onDestroy() {
        if (guide != null) {
            guide.stopLoading();
            guide.destroy();
            guide = null;
        }
        super.onDestroy();
    }
}
