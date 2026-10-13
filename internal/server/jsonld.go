package server

// Schema.org structured data (JSON-LD) for rich results. html/template serialises
// these maps safely inside <script type="application/ld+json">.

type ld = map[string]any

func websiteLD(base string) ld {
	return ld{
		"@context":      "https://schema.org",
		"@type":         "WebSite",
		"name":          siteName,
		"alternateName": siteName + " " + siteTagline,
		"url":           base + "/",
		"potentialAction": ld{
			"@type":       "SearchAction",
			"target":      ld{"@type": "EntryPoint", "urlTemplate": base + "/?q={search_term_string}"},
			"query-input": "required name=search_term_string",
		},
	}
}

func organizationLD(base, logo string) ld {
	return ld{
		"@context": "https://schema.org",
		"@type":    "Organization",
		"name":     siteName,
		"url":      base + "/",
		"logo":     absURL(base, logo),
	}
}

func breadcrumbLD(base string, crumbs []crumb) ld {
	items := make([]ld, len(crumbs))
	for i, c := range crumbs {
		items[i] = ld{"@type": "ListItem", "position": i + 1, "name": c.Name, "item": base + c.URL}
	}
	return ld{"@context": "https://schema.org", "@type": "BreadcrumbList", "itemListElement": items}
}

func collectionLD(base, path, name string) ld {
	return ld{
		"@context": "https://schema.org",
		"@type":    "CollectionPage",
		"name":     name,
		"url":      base + path,
		"isPartOf": ld{"@type": "WebSite", "name": siteName, "url": base + "/"},
	}
}

func templateListLD(base string, list []templateJSON) ld {
	items := make([]ld, len(list))
	for i, t := range list {
		items[i] = ld{"@type": "ListItem", "position": i + 1, "url": base + t.URL, "name": memeName(t.Name) + " meme template"}
	}
	return ld{"@context": "https://schema.org", "@type": "ItemList", "itemListElement": items}
}

func templateLD(base string, t templateJSON, description string) ld {
	img := ld{
		"@context":            "https://schema.org",
		"@type":               "ImageObject",
		"name":                memeName(t.Name) + " meme template",
		"description":         description,
		"contentUrl":          absURL(base, t.ImageURL),
		"url":                 base + t.URL,
		"width":               t.Width,
		"height":              t.Height,
		"uploadDate":          isoDate(t.CreatedAt),
		"isAccessibleForFree": true,
	}
	if len(t.Tags) > 0 {
		img["keywords"] = append([]string{"meme template", t.Name}, t.Tags...)
	}
	if t.UploadedBy != "" {
		img["author"] = ld{"@type": "Person", "name": t.UploadedBy}
	}
	return img
}

func memeLD(base string, m memeJSON, title string) ld {
	img := ld{
		"@context":      "https://schema.org",
		"@type":         "ImageObject",
		"name":          title,
		"contentUrl":    absURL(base, m.ImageURL),
		"url":           base + m.ShareURL,
		"width":         m.Width,
		"height":        m.Height,
		"uploadDate":    isoDate(m.CreatedAt),
		"datePublished": isoDate(m.CreatedAt),
		"author":        ld{"@type": "Person", "name": m.Author},
		"creator":       ld{"@type": "Person", "name": m.Author},
		"interactionStatistic": ld{
			"@type":                "InteractionCounter",
			"interactionType":      "https://schema.org/LikeAction",
			"userInteractionCount": m.Likes,
		},
	}
	if m.TemplateURL != "" {
		img["isBasedOn"] = base + m.TemplateURL
	}
	return img
}
